package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadAuthReadsDiscordBotTokenFromEnvironment(t *testing.T) {
	secret := "env-token-secret"
	t.Setenv("DISCORD_BOT_TOKEN", secret)

	auth, err := loadAuth(AuthOptions{})
	if err != nil {
		t.Fatalf("loadAuth returned error: %v", err)
	}
	if auth.BotToken != secret {
		t.Fatalf("BotToken = %q, want %q", auth.BotToken, secret)
	}
	if strings.Contains(fmt.Sprintf("%v", auth), secret) || strings.Contains(fmt.Sprintf("%#v", auth), secret) {
		t.Fatalf("formatted auth leaks token: %%v=%q %%#v=%q", fmt.Sprintf("%v", auth), fmt.Sprintf("%#v", auth))
	}
}

func TestLoadAuthReadsDiscordBotTokenFromEnvFile(t *testing.T) {
	withoutDiscordBotToken(t)
	secret := "file-token-secret"
	envFile := filepath.Join(t.TempDir(), ".env")
	content := "# local development auth\nexport DISCORD_BOT_TOKEN=\"" + secret + "\"\nOTHER_VALUE=ignored\n"
	if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	auth, err := loadAuth(AuthOptions{EnvFile: envFile})
	if err != nil {
		t.Fatalf("loadAuth returned error: %v", err)
	}
	if auth.BotToken != secret {
		t.Fatalf("BotToken = %q, want %q", auth.BotToken, secret)
	}
}

func TestLoadAuthRedactsEnvFileParseErrors(t *testing.T) {
	withoutDiscordBotToken(t)
	secret := "parse-error-secret"
	envFile := filepath.Join(t.TempDir(), ".env")
	content := "DISCORD_BOT_TOKEN=" + secret + "\nnot a valid line containing " + secret + "\n"
	if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := loadAuth(AuthOptions{EnvFile: envFile})
	if err == nil {
		t.Fatal("loadAuth returned nil error for malformed env file")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("parse error leaks secret token: %v", err)
	}
}

func TestDiscordClientTypedHelpersUseRESTPathsAndHeaders(t *testing.T) {
	token := "client-token-secret"
	var requests []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bot "+token; got != want {
			t.Errorf("Authorization header = %q, want %q", got, want)
		}
		if got := r.Header.Get("User-Agent"); got == "" {
			t.Error("User-Agent header is empty")
		}
		if r.Method != http.MethodGet && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			t.Errorf("Content-Type header = %q, want application/json", r.Header.Get("Content-Type"))
		}

		requests = append(requests, r.Method+" "+r.URL.EscapedPath())
		w.Header().Set("Content-Type", "application/json")

		switch r.Method + " " + r.URL.Path {
		case "GET /users/@me":
			json.NewEncoder(w).Encode(DiscordUser{ID: "u1", Username: "admin-bot"})
		case "GET /channels/c1":
			json.NewEncoder(w).Encode(DiscordChannel{ID: "c1", Name: "general", Type: ChannelTypeGuildText})
		case "GET /guilds/g1/channels":
			json.NewEncoder(w).Encode([]DiscordChannel{{ID: "c1", Name: "general", Type: ChannelTypeGuildText}})
		case "POST /guilds/g1/channels":
			var req CreateGuildChannelRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode create request: %v", err)
			}
			if req.Name != "new-channel" || req.Type != ChannelTypeGuildText {
				t.Errorf("create request = %+v, want name=new-channel type=text", req)
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(DiscordChannel{ID: "c2", Name: req.Name, Type: req.Type})
		case "PATCH /channels/c1":
			var req UpdateChannelRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode update request: %v", err)
			}
			if req.Name == nil || *req.Name != "renamed" {
				t.Errorf("update request name = %v, want renamed", req.Name)
			}
			json.NewEncoder(w).Encode(DiscordChannel{ID: "c1", Name: *req.Name, Type: ChannelTypeGuildText})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewDiscordClient(token, WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	ctx := context.Background()

	user, err := client.GetCurrentUser(ctx)
	if err != nil {
		t.Fatalf("GetCurrentUser returned error: %v", err)
	}
	if user.ID != "u1" || user.Username != "admin-bot" {
		t.Fatalf("user = %+v, want admin bot", user)
	}

	channel, err := client.GetChannel(ctx, "c1")
	if err != nil {
		t.Fatalf("GetChannel returned error: %v", err)
	}
	if channel.ID != "c1" || channel.Name != "general" {
		t.Fatalf("channel = %+v, want c1/general", channel)
	}

	channels, err := client.GetGuildChannels(ctx, "g1")
	if err != nil {
		t.Fatalf("GetGuildChannels returned error: %v", err)
	}
	if len(channels) != 1 || channels[0].ID != "c1" {
		t.Fatalf("channels = %+v, want c1", channels)
	}

	created, err := client.CreateGuildChannel(ctx, "g1", CreateGuildChannelRequest{Name: "new-channel", Type: ChannelTypeGuildText})
	if err != nil {
		t.Fatalf("CreateGuildChannel returned error: %v", err)
	}
	if created.ID != "c2" || created.Name != "new-channel" {
		t.Fatalf("created channel = %+v, want c2/new-channel", created)
	}

	renamed := "renamed"
	updated, err := client.UpdateChannel(ctx, "c1", UpdateChannelRequest{Name: &renamed})
	if err != nil {
		t.Fatalf("UpdateChannel returned error: %v", err)
	}
	if updated.ID != "c1" || updated.Name != "renamed" {
		t.Fatalf("updated channel = %+v, want c1/renamed", updated)
	}

	wantRequests := []string{
		"GET /users/@me",
		"GET /channels/c1",
		"GET /guilds/g1/channels",
		"POST /guilds/g1/channels",
		"PATCH /channels/c1",
	}
	if !reflect.DeepEqual(requests, wantRequests) {
		t.Fatalf("requests = %v, want %v", requests, wantRequests)
	}
}

func TestDiscordClientErrorFormattingRedactsToken(t *testing.T) {
	token := "client-token-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{
			"message": "Missing Permissions for " + token,
			"code":    50013,
		})
	}))
	defer server.Close()

	client := NewDiscordClient(token, WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	_, err := client.GetChannel(context.Background(), "c1")
	if err == nil {
		t.Fatal("GetChannel returned nil error")
	}

	message := err.Error()
	for _, want := range []string{"GET", "/channels/c1", "403", "Missing Permissions", "50013"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error %q missing %q", message, want)
		}
	}
	if strings.Contains(message, token) {
		t.Fatalf("error leaks token %q: %v", token, err)
	}
}

func withoutDiscordBotToken(t *testing.T) {
	t.Helper()
	previous, hadPrevious := os.LookupEnv("DISCORD_BOT_TOKEN")
	if err := os.Unsetenv("DISCORD_BOT_TOKEN"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadPrevious {
			_ = os.Setenv("DISCORD_BOT_TOKEN", previous)
		} else {
			_ = os.Unsetenv("DISCORD_BOT_TOKEN")
		}
	})
}
