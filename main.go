package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"maunium.net/go/mautrix/appservice"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/joenas/matrix-slackhooks/internal/bridge"
	"github.com/joenas/matrix-slackhooks/internal/config"
	"github.com/joenas/matrix-slackhooks/internal/store"
	"github.com/joenas/matrix-slackhooks/internal/webhook"
)

const asID = "slackhooks"

const usage = `Usage: slackhooks [-config config.yaml] <command> [args]

Commands:
  generate-registration [-out registration.yaml]  Generate the appservice registration file
  add-hook [-label label] [-by user] <room-id>    Add a webhook for a room
  start                                           Run the appservice + webhook server (default)
`

func main() {
	log.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).
		With().Timestamp().Logger()

	configPath := flag.String("config", "config.yaml", "config file path")
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

func loadConfig(path string) *config.Config {
	cfg, err := config.Load(path)
	if err != nil {
		if !(errors.Is(err, os.ErrNotExist) && path == "config.yaml") {
			log.Fatal().Err(err).Msg("Failed to load config")
		}
		cfg = config.Default()
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
	reg.Namespaces.UserIDs = appservice.NamespaceList{
		{Regex: "^" + cfg.UserPrefix + `.*$`, Exclusive: true},
		{Regex: "^" + cfg.BotLocalpart + "$", Exclusive: true},
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

func cmdAddHook(configPath string, args []string) {
	fs := flag.NewFlagSet("add-hook", flag.ExitOnError)
	label := fs.String("label", "", "optional label for the webhook (used as display name fallback)")
	createdBy := fs.String("by", os.Getenv("USER"), "who is creating this webhook")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		log.Fatal().Msg("usage: slackhooks add-hook [-label label] [-by user] <room-id>")
	}
	roomID := fs.Arg(0)

	cfg := loadConfig(configPath)
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to open database")
	}
	defer db.Close()

	var lastErr error
	for i := 0; i < 3; i++ {
		hook := &store.Hook{
			Token:     store.NewToken(),
			RoomID:    id.RoomID(roomID),
			Label:     *label,
			CreatedBy: *createdBy,
			CreatedAt: time.Now(),
		}
		if err = db.InsertHook(hook); err == nil {
			fmt.Printf("Room:  %s\n", roomID)
			fmt.Printf("URL:   %s/hooks/%s\n", cfg.WebhookBaseURL(), hook.Token)
			fmt.Printf("Token: %s\n", hook.Token)
			return
		}
		lastErr = err
	}
	log.Fatal().Err(lastErr).Msg("Failed to insert hook")
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
