package main

import (
	"regexp"
	"testing"
)

func TestUserNamespaceRegexes(t *testing.T) {
	userRaw, botRaw := userNamespaceRegexes("_slackhook_", "slackhooks", "localhost")
	userRe, err := regexp.Compile(userRaw)
	if err != nil {
		t.Fatalf("user namespace regex %q does not compile: %v", userRaw, err)
	}
	botRe, err := regexp.Compile(botRaw)
	if err != nil {
		t.Fatalf("bot namespace regex %q does not compile: %v", botRaw, err)
	}

	userMatches := []string{
		"@_slackhook_x:localhost",
		"@_slackhook_build-bot:localhost",
		"@_slackhook_:localhost",
	}
	for _, userID := range userMatches {
		if !userRe.MatchString(userID) {
			t.Errorf("user regex %q should match %s", userRaw, userID)
		}
	}
	userNonMatches := []string{
		"@slackhooks:localhost",
		"@_slackhook_x:example.com",
		"@someone:localhost",
		"@prefix_slackhook_x:localhost",
		"@_slackhook_x:localhost:1234",
	}
	for _, userID := range userNonMatches {
		if userRe.MatchString(userID) {
			t.Errorf("user regex %q should not match %s", userRaw, userID)
		}
	}

	if !botRe.MatchString("@slackhooks:localhost") {
		t.Errorf("bot regex %q should match the bot user", botRaw)
	}
	for _, userID := range []string{
		"@slackhooks:example.com",
		"@slackhooks_extra:localhost",
		"@_slackhook_x:localhost",
		"@slackhooks:localhost:1234",
	} {
		if botRe.MatchString(userID) {
			t.Errorf("bot regex %q should not match %s", botRaw, userID)
		}
	}
}

func TestUserNamespaceRegexesQuoted(t *testing.T) {
	userRaw, botRaw := userNamespaceRegexes("_slack.hook+", "bot.name", "ex ample.com")
	if !regexp.MustCompile(userRaw).MatchString("@_slack.hook+x:ex ample.com") {
		t.Errorf("regex %q should match a prefixed user with meta characters", userRaw)
	}
	if regexp.MustCompile(userRaw).MatchString("@_slackxhook+x:ex ample.com") {
		t.Errorf("regex %q should not match an unescaped variation", userRaw)
	}
	if !regexp.MustCompile(botRaw).MatchString("@bot.name:ex ample.com") {
		t.Errorf("bot regex %q should match the bot user with meta characters", botRaw)
	}
}
