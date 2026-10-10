package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/joenas/matrix-slackhooks/internal/bridge"
	"github.com/joenas/matrix-slackhooks/internal/config"
	"github.com/joenas/matrix-slackhooks/internal/store"
	"github.com/joenas/matrix-slackhooks/internal/webhook"
)

const asID = "slackhooks"

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `Usage: slackhooks [-config config.yaml] <command> [args]

Commands:
  generate-registration [-out registration.yaml]  Generate the appservice registration file
  add-hook [-label label] [-by user] <room>       Add a webhook for a room (!roomid:server or #alias:server)
  list-hooks [room-id]                            List configured webhooks (optionally for one room)
  remove-hook <token-or-prefix>                   Delete a webhook by token or unique prefix
  backup <path>                                   Write a consistent database snapshot (safe while running)
  healthcheck                                     Probe the appservice liveness endpoint (exit 0 if healthy)
  version                                         Print the version
  start                                           Run the appservice + webhook server (default)
`

func main() {
	log.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).
		With().Timestamp().Logger()

	configPath := flag.String("config", config.DefaultPath(), "config file path (default: $SLACKHOOKS_CONFIG or config.yaml)")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		runStart(*configPath, nil)
		return
	}
	cmd, cmdArgs := args[0], args[1:]
	switch cmd {
	case "generate-registration":
		cmdGenerateRegistration(*configPath, cmdArgs)
	case "add-hook":
		cmdAddHook(*configPath, cmdArgs)
	case "list-hooks":
		cmdListHooks(*configPath, cmdArgs)
	case "remove-hook":
		cmdRemoveHook(*configPath, cmdArgs)
	case "backup":
		cmdBackup(*configPath, cmdArgs)
	case "healthcheck":
		cmdHealthcheck(*configPath, cmdArgs)
	case "version":
		fmt.Println(version)
	case "start":
		runStart(*configPath, cmdArgs)
	case "-h", "--help", "help":
		flag.Usage()
	default:
		log.Error().Str("command", cmd).Msg("Unknown command")
		flag.Usage()
		os.Exit(2)
	}
}

// openConfig loads the config file, treating a missing file at the effective
// default path (config.yaml or $SLACKHOOKS_CONFIG) as "defaults plus env
// overrides". An explicit -config path is required to exist.
func openConfig(path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if err == nil || !(errors.Is(err, os.ErrNotExist) && path == config.DefaultPath()) {
		return cfg, err
	}
	return config.Load("")
}

func loadConfig(path string) *config.Config {
	cfg, err := openConfig(path)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load config")
	}
	if err = cfg.Validate(); err != nil {
		log.Fatal().Err(err).Msg("Invalid config")
	}
	return cfg
}

func cmdGenerateRegistration(configPath string, args []string) {
	fs := flag.NewFlagSet("generate-registration", flag.ExitOnError)
	outPath := fs.String("out", "", "file to write the registration to (default: stdout)")
	_ = fs.Parse(args)

	cfg := loadConfig(configPath)
	generated := false
	if cfg.ASToken == "" {
		cfg.ASToken = store.NewToken() + store.NewToken()
		generated = true
	}
	if cfg.HSToken == "" {
		cfg.HSToken = store.NewToken() + store.NewToken()
		generated = true
	}
	f := false
	reg := &appservice.Registration{
		ID:              asID,
		URL:             cfg.RegistrationURL(),
		AppToken:        cfg.ASToken,
		ServerToken:     cfg.HSToken,
		SenderLocalpart: cfg.BotLocalpart,
		RateLimited:     &f,
	}
	userRegex, botRegex := userNamespaceRegexes(cfg.UserPrefix, cfg.BotLocalpart, cfg.ServerName)
	reg.Namespaces.UserIDs = appservice.NamespaceList{
		{Regex: userRegex, Exclusive: true},
		{Regex: botRegex, Exclusive: true},
	}
	data, err := reg.YAML()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to render registration")
	}
	if *outPath != "" {
		if err = os.WriteFile(*outPath, []byte(data), 0o600); err != nil {
			log.Fatal().Err(err).Msg("Failed to write registration file")
		}
		log.Info().Str("file", *outPath).Msg("Wrote registration file")
	} else {
		fmt.Print(data)
	}
	if generated {
		fmt.Fprintln(os.Stderr, "\n# The as_token/hs_token were randomly generated.")
		fmt.Fprintf(os.Stderr, "# Add them to your config file (%s):\n", configPath)
		fmt.Fprintf(os.Stderr, "# as_token: %s\n", cfg.ASToken)
		fmt.Fprintf(os.Stderr, "# hs_token: %s\n", cfg.HSToken)
	}
}

// userNamespaceRegexes builds the user namespace regexes for the appservice
// registration. Synapse matches these against full user IDs (@localpart:
// servername), so the patterns must include the sigil and the server name.
func userNamespaceRegexes(userPrefix, botLocalpart, serverName string) (userRegex, botRegex string) {
	userRegex = "^@" + regexp.QuoteMeta(userPrefix) + ".*:" + regexp.QuoteMeta(serverName) + "$"
	botRegex = "^@" + regexp.QuoteMeta(botLocalpart) + ":" + regexp.QuoteMeta(serverName) + "$"
	return
}

// roomIDPattern matches a Matrix room ID: !opaque:server_name.
var roomIDPattern = regexp.MustCompile(`^![^:]+:.+$`)

// resolveRoom turns a room ID (!localpart:server_name) or room alias
// (#localpart:server_name) into a room ID. Aliases need a configured token to
// resolve through the homeserver; without one the caller must pass a room ID.
func resolveRoom(ctx context.Context, cfg *config.Config, arg string) (id.RoomID, error) {
	switch {
	case strings.HasPrefix(arg, "!"):
		if !roomIDPattern.MatchString(arg) {
			return "", fmt.Errorf("%q is not a valid room ID (expected !localpart:server_name)", arg)
		}
		return id.RoomID(arg), nil
	case strings.HasPrefix(arg, "#"):
		if cfg.ASToken == "" {
			return "", fmt.Errorf("cannot resolve alias %q: as_token is not configured; pass a room ID (!localpart:server_name) instead", arg)
		}
		cli, err := mautrix.NewClient(cfg.HomeserverURL, id.NewUserID(cfg.BotLocalpart, cfg.ServerName), cfg.ASToken)
		if err != nil {
			return "", fmt.Errorf("create homeserver client: %w", err)
		}
		resp, err := cli.ResolveAlias(ctx, id.RoomAlias(arg))
		if err != nil {
			return "", fmt.Errorf("resolve alias %s: %w", arg, err)
		} else if resp.RoomID == "" {
			return "", fmt.Errorf("alias %s resolved to no room", arg)
		}
		return resp.RoomID, nil
	default:
		return "", fmt.Errorf("%q is not a room ID (!localpart:server_name) or alias (#localpart:server_name)", arg)
	}
}

// defaultCreatedBy is the default for add-hook's -by flag: $USER, falling
// back to "cli" when it is unset (e.g. inside the Docker image, where there
// is no $USER), so hooks never get an empty "created by".
func defaultCreatedBy() string {
	if user := os.Getenv("USER"); user != "" {
		return user
	}
	return "cli"
}

func cmdAddHook(configPath string, args []string) {
	fs := flag.NewFlagSet("add-hook", flag.ExitOnError)
	label := fs.String("label", "", "optional label for the webhook (used as display name fallback)")
	createdBy := fs.String("by", defaultCreatedBy(), `who is creating this webhook (default: $USER, or "cli" when unset)`)
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		log.Fatal().Msg("usage: slackhooks add-hook [-label label] [-by user] <room>")
	}

	cfg := loadConfig(configPath)
	roomID, err := resolveRoom(context.Background(), cfg, fs.Arg(0))
	if err != nil {
		log.Fatal().Err(err).Msg("Invalid room")
	}

	normalized, err := store.NormalizeLabel(*label)
	if err != nil {
		log.Fatal().Err(err).Msg("Invalid label")
	}

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to open database")
	}
	defer db.Close()

	var lastErr error
	for i := 0; i < 3; i++ {
		hook := &store.Hook{
			Token:     store.NewToken(),
			RoomID:    roomID,
			Label:     normalized,
			CreatedBy: *createdBy,
			CreatedAt: time.Now(),
		}
		if err = db.InsertHook(hook); err == nil {
			fmt.Printf("Room:  %s\n", roomID)
			fmt.Printf("URL:   %s/hooks/%s\n", cfg.WebhookBaseURL(), hook.Token)
			fmt.Printf("Token: %s\n", hook.Token)
			fmt.Printf("Next:  invite @%s:%s to %s (it will auto-join)\n", cfg.BotLocalpart, cfg.ServerName, roomID)
			return
		}
		lastErr = err
	}
	log.Fatal().Err(lastErr).Msg("Failed to insert hook")
}

func cmdListHooks(configPath string, args []string) {
	fs := flag.NewFlagSet("list-hooks", flag.ExitOnError)
	_ = fs.Parse(args)
	if fs.NArg() > 1 {
		log.Fatal().Msg("usage: slackhooks list-hooks [room-id]")
	}
	var roomID id.RoomID
	if fs.NArg() == 1 {
		roomID = id.RoomID(fs.Arg(0))
	}

	cfg := loadConfig(configPath)
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to open database")
	}
	defer db.Close()

	hooks, err := db.ListHooks(roomID)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to list hooks")
	} else if len(hooks) == 0 {
		fmt.Println("No webhooks configured.")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "TOKEN\tROOM\tLABEL\tCREATED BY\tCREATED\tURL")
	for _, hook := range hooks {
		prefix := hook.Token
		if len(prefix) > 8 {
			prefix = prefix[:8]
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			prefix, hook.RoomID, hook.Label, hook.CreatedBy,
			hook.CreatedAt.UTC().Format(time.RFC3339),
			cfg.WebhookBaseURL()+"/hooks/"+hook.Token)
	}
	_ = w.Flush()
}

func cmdRemoveHook(configPath string, args []string) {
	fs := flag.NewFlagSet("remove-hook", flag.ExitOnError)
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		log.Fatal().Msg("usage: slackhooks remove-hook <token-or-unique-prefix>")
	}
	prefix := fs.Arg(0)
	if len(prefix) < bridge.MinRemovePrefix {
		log.Fatal().Str("prefix", prefix).Int("min", bridge.MinRemovePrefix).Msg("Prefix is too short")
	}

	cfg := loadConfig(configPath)
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to open database")
	}
	defer db.Close()

	matches, err := db.FindHooksByPrefix(prefix)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to look up hooks")
	} else if len(matches) == 0 {
		log.Fatal().Str("prefix", prefix).Msg("No webhook matches that token or prefix")
	} else if len(matches) > 1 {
		fmt.Fprintf(os.Stderr, "%d webhooks match %q; use a longer prefix:\n", len(matches), prefix)
		for _, hook := range matches {
			token := hook.Token
			if len(token) > 12 {
				token = token[:12]
			}
			fmt.Fprintf(os.Stderr, "  %s  %s\n", token, hook.RoomID)
		}
		os.Exit(1)
	}

	hook := matches[0]
	if err = db.DeleteHook(hook.Token); err != nil {
		log.Fatal().Err(err).Msg("Failed to delete hook")
	}
	fmt.Printf("Removed webhook for room %s (token %s)\n", hook.RoomID, hook.Token)
}

func cmdBackup(configPath string, args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		log.Fatal().Msg("usage: slackhooks backup <path>")
	}
	dest := fs.Arg(0)

	cfg := loadConfig(configPath)
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to open database")
	}
	defer db.Close()

	if err = db.Backup(dest); err != nil {
		log.Fatal().Err(err).Msg("Backup failed")
	}
	fmt.Printf("Backup written to %s\n", dest)
}

// cmdHealthcheck probes the appservice liveness endpoint, for use as a
// Docker HEALTHCHECK. It needs no config file, only the port the appservice
// listens on.
func cmdHealthcheck(configPath string, args []string) {
	fs := flag.NewFlagSet("healthcheck", flag.ExitOnError)
	_ = fs.Parse(args)

	cfg, err := openConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: cannot load config: %v\n", err)
		os.Exit(1)
	}
	port := parseHostConfig(cfg.ASAddress).Port
	if port == 0 {
		fmt.Fprintf(os.Stderr, "healthcheck: no TCP port in as_address %q\n", cfg.ASAddress)
		os.Exit(1)
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/_matrix/mau/live", port)
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: GET %s failed: %v\n", url, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: GET %s returned %s\n", url, resp.Status)
		os.Exit(1)
	}
}

func parseHostConfig(addr string) appservice.HostConfig {
	host := appservice.HostConfig{Hostname: addr}
	if strings.HasPrefix(addr, "/") {
		return host
	}
	idx := strings.LastIndex(addr, ":")
	if idx >= 0 {
		host.Hostname = addr[:idx]
		if port, err := strconv.ParseUint(addr[idx+1:], 10, 16); err == nil {
			host.Port = uint16(port)
		}
	}
	return host
}

func runStart(configPath string, args []string) {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	_ = fs.Parse(args)

	log.Info().Str("version", version).Msg("Starting slackhooks")
	cfg := loadConfig(configPath)
	if err := cfg.ValidateTokens(); err != nil {
		log.Fatal().Err(err).Msg("Cannot start")
	}

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to open database")
	}
	defer db.Close()

	as, err := appservice.CreateFull(appservice.CreateOpts{
		Registration: &appservice.Registration{
			ID:              asID,
			URL:             cfg.RegistrationURL(),
			AppToken:        cfg.ASToken,
			ServerToken:     cfg.HSToken,
			SenderLocalpart: cfg.BotLocalpart,
		},
		HomeserverDomain: cfg.ServerName,
		HomeserverURL:    cfg.HomeserverURL,
		HostConfig:       parseHostConfig(cfg.ASAddress),
	})
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create appservice")
	}
	as.Log = log.Logger
	as.StateStore = store.NewStateStore(db)

	br := bridge.New(cfg, db, as, log.Logger)
	as.QueryHandler = br

	handler := &webhook.Handler{Store: db, Sender: br, Log: log.Logger}
	webhook.Mount(as.Router, handler)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ep := appservice.NewEventProcessor(as)
	ep.On(event.StateMember, br.HandleRoomMember)
	ep.On(event.EventMessage, br.HandleBotMessage)
	ep.Start(ctx)

	go as.Start()
	go br.WarnDisallowedJoinedRooms(ctx)
	var webhookServer *http.Server
	if cfg.WebhookAddress != "" && cfg.WebhookAddress != cfg.ASAddress {
		webhookMux := http.NewServeMux()
		webhook.Mount(webhookMux, handler)
		host := parseHostConfig(cfg.WebhookAddress)
		webhookServer = &http.Server{Addr: host.Address(), Handler: webhookMux}
		go func() {
			log.Info().Str("address", host.Address()).Msg("Starting webhook HTTP listener")
			if err := webhookServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error().Err(err).Msg("Error in webhook HTTP listener")
			}
		}()
	}

	if cfg.BotDisplayName != "" {
		go func() {
			botCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := as.BotIntent().SetDisplayName(botCtx, cfg.BotDisplayName); err != nil {
				log.Warn().Err(err).Msg("Failed to set bot display name")
			}
		}()
	}

	log.Info().
		Str("as_address", cfg.ASAddress).
		Str("db", cfg.DBPath).
		Msg("Slackhooks is now running")
	<-ctx.Done()
	log.Info().Msg("Shutting down")
	as.Stop()
	if webhookServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = webhookServer.Shutdown(shutdownCtx)
	}
}
