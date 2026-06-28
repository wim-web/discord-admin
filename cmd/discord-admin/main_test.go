package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunPrintsUsageForHelp(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run([]string{"--help"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("run --help exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{
		"Usage: discord-admin <command> [options]",
		"list",
		"snapshot",
		"plan init",
		"apply <plan.json>",
		"--env-file <path>",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("help output missing %q; output:\n%s", want, output)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunAcceptsEnvFileGlobalOptionWithHelp(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	envFile := filepath.Join(t.TempDir(), ".env")

	exitCode := run([]string{"--env-file", envFile, "--help"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("run --env-file --help exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "DISCORD_BOT_TOKEN") {
		t.Fatalf("help output missing auth environment details; output:\n%s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunListPrintsGuildChannelsAsTree(t *testing.T) {
	withoutDiscordBotToken(t)
	token := "list-token-secret"
	envFile := writeTestEnvFile(t, map[string]string{discordBotTokenEnv: token})
	parentID := "cat1"
	channels := []DiscordChannel{
		{ID: "voice1", Name: "Lounge", Type: ChannelTypeGuildVoice, ParentID: &parentID, Position: 2},
		{ID: "cat1", Name: "Alpha", Type: ChannelTypeGuildCategory, Position: 1},
		{ID: "text1", Name: "general", Type: ChannelTypeGuildText, ParentID: &parentID, Position: 1},
		{ID: "orphan1", Name: "uncategorized", Type: ChannelTypeGuildText, Position: 3},
	}
	server, requests := guildChannelsTestServer(t, token, "g1", channels)
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := runWithDependencies(
		[]string{"--env-file", envFile, "list", "--guild-id", "g1"},
		&stdout,
		&stderr,
		runDependencies{ClientFactory: testClientFactory(server, token)},
	)

	if exitCode != 0 {
		t.Fatalf("run list exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
	}
	if !reflect.DeepEqual(*requests, []string{"GET /guilds/g1/channels"}) {
		t.Fatalf("requests = %v, want read-only guild channel GET", *requests)
	}
	output := stdout.String()
	assertContainsInOrder(t, output, []string{
		"Guild g1",
		"[category] Alpha (cat1)",
		"  [text] general (text1)",
		"  [voice] Lounge (voice1)",
		"[text] uncategorized (orphan1)",
	})
	if strings.Contains(output, token) || strings.Contains(stderr.String(), token) {
		t.Fatalf("command output leaks token; stdout=%q stderr=%q", output, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunSnapshotWritesGuildChannelStateJSON(t *testing.T) {
	withoutDiscordBotToken(t)
	token := "snapshot-token-secret"
	envFile := writeTestEnvFile(t, map[string]string{discordBotTokenEnv: token})
	parentID := "cat1"
	channels := []DiscordChannel{
		{ID: "cat1", Name: "Alpha", Type: ChannelTypeGuildCategory, Position: 0},
		{ID: "text1", Name: "general", Type: ChannelTypeGuildText, ParentID: &parentID, Position: 0},
	}
	server, requests := guildChannelsTestServer(t, token, "g1", channels)
	defer server.Close()
	outFile := filepath.Join(t.TempDir(), "snapshot.json")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := runWithDependencies(
		[]string{"--env-file", envFile, "snapshot", "--guild-id", "g1", "--out", outFile},
		&stdout,
		&stderr,
		runDependencies{ClientFactory: testClientFactory(server, token)},
	)

	if exitCode != 0 {
		t.Fatalf("run snapshot exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
	}
	if !reflect.DeepEqual(*requests, []string{"GET /guilds/g1/channels"}) {
		t.Fatalf("requests = %v, want read-only guild channel GET", *requests)
	}
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var snapshot struct {
		GuildID  string           `json:"guild_id"`
		Channels []map[string]any `json:"channels"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("snapshot is not valid JSON: %v; content=%s", err, data)
	}
	if snapshot.GuildID != "g1" {
		t.Fatalf("snapshot guild_id = %q, want g1", snapshot.GuildID)
	}
	if len(snapshot.Channels) != 2 {
		t.Fatalf("snapshot channels = %d, want 2; content=%s", len(snapshot.Channels), data)
	}
	for _, field := range []string{"id", "name", "type", "parent_id", "position"} {
		if _, ok := snapshot.Channels[0][field]; !ok {
			t.Fatalf("snapshot channel missing %q field: %#v", field, snapshot.Channels[0])
		}
	}
	if strings.Contains(string(data), token) || strings.Contains(stdout.String(), token) || strings.Contains(stderr.String(), token) {
		t.Fatalf("snapshot command leaks token; stdout=%q stderr=%q content=%s", stdout.String(), stderr.String(), data)
	}
}

func TestRunListRequiresGuildIDUnlessExplicitlyConfigured(t *testing.T) {
	withoutDiscordBotToken(t)
	token := "guild-config-token-secret"
	envFileWithoutGuild := writeTestEnvFile(t, map[string]string{discordBotTokenEnv: token})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := runWithDependencies(
		[]string{"--env-file", envFileWithoutGuild, "list"},
		&stdout,
		&stderr,
		runDependencies{ClientFactory: func(_ string) guildChannelsClient {
			t.Fatal("client factory should not be called when guild id is missing")
			return nil
		}},
	)
	if exitCode != 2 {
		t.Fatalf("run list without guild id exit code = %d, want 2", exitCode)
	}
	if !strings.Contains(stderr.String(), "--guild-id") || stdout.Len() != 0 {
		t.Fatalf("missing guild id output mismatch; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	envFileWithGuild := writeTestEnvFile(t, map[string]string{
		discordBotTokenEnv: "configured-guild-token-secret",
		guildIDEnv:         "g1",
	})
	server, _ := guildChannelsTestServer(t, "configured-guild-token-secret", "g1", nil)
	defer server.Close()
	stdout.Reset()
	stderr.Reset()
	exitCode = runWithDependencies(
		[]string{"--env-file", envFileWithGuild, "list"},
		&stdout,
		&stderr,
		runDependencies{ClientFactory: testClientFactory(server, "configured-guild-token-secret")},
	)
	if exitCode != 0 {
		t.Fatalf("run list with configured guild id exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Guild g1") {
		t.Fatalf("list output missing configured guild id; output=%q", stdout.String())
	}
}

func guildChannelsTestServer(t *testing.T, token string, guildID string, channels []DiscordChannel) (*httptest.Server, *[]string) {
	t.Helper()
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bot "+token; got != want {
			t.Errorf("Authorization header = %q, want %q", got, want)
		}
		if r.Method != http.MethodGet {
			t.Errorf("read-only command used %s %s", r.Method, r.URL.Path)
		}
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/guilds/"+guildID+"/channels" {
			if channels == nil {
				channels = []DiscordChannel{}
			}
			if err := json.NewEncoder(w).Encode(channels); err != nil {
				t.Errorf("encode response: %v", err)
			}
			return
		}
		http.NotFound(w, r)
	}))
	return server, &requests
}

func testClientFactory(server *httptest.Server, wantToken string) func(string) guildChannelsClient {
	return func(token string) guildChannelsClient {
		if token != wantToken {
			panic("client factory received unexpected token")
		}
		return NewDiscordClient(token, WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	}
}

func writeTestEnvFile(t *testing.T, values map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	var content strings.Builder
	for key, value := range values {
		content.WriteString(key)
		content.WriteString("=")
		content.WriteString(value)
		content.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertContainsInOrder(t *testing.T, text string, wants []string) {
	t.Helper()
	lastIndex := -1
	for _, want := range wants {
		index := strings.Index(text, want)
		if index == -1 {
			t.Fatalf("output missing %q; output:\n%s", want, text)
		}
		if index < lastIndex {
			t.Fatalf("output contains %q out of order; output:\n%s", want, text)
		}
		lastIndex = index
	}
}
