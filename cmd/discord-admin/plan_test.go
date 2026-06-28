package main

import (
	"bytes"
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

func TestRunPlanInitPrintsSamplePlanJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run([]string{"plan", "init"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("run plan init exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	var plan struct {
		GuildID    string `json:"guild_id"`
		Operations []struct {
			Op        string `json:"op"`
			Name      string `json:"name,omitempty"`
			ChannelID string `json:"channel_id,omitempty"`
			ParentID  string `json:"parent_id,omitempty"`
			Position  *int   `json:"position,omitempty"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		t.Fatalf("plan init output is not valid JSON: %v; output=%s", err, stdout.String())
	}
	if plan.GuildID == "" {
		t.Fatalf("sample plan guild_id is empty; output=%s", stdout.String())
	}

	gotOps := map[string]bool{}
	for _, operation := range plan.Operations {
		gotOps[operation.Op] = true
	}
	for _, want := range []string{"create_category", "create_text_channel", "rename_channel", "move_channel", "set_position"} {
		if !gotOps[want] {
			t.Fatalf("sample plan missing %q operation; output=%s", want, stdout.String())
		}
	}
}

func TestRunApplyDryRunPrintsDiffAndDoesNotCallDiscordClient(t *testing.T) {
	planPath := writePlanFile(t, `{
		"guild_id": "g1",
		"operations": [
			{"op": "create_category", "name": "Projects", "position": 0},
			{"op": "create_text_channel", "name": "alpha", "parent_id": "cat1", "position": 1},
			{"op": "rename_channel", "channel_id": "old1", "name": "new-name"},
			{"op": "move_channel", "channel_id": "move1", "parent_id": "cat2"},
			{"op": "set_position", "channel_id": "pos1", "position": 4}
		]
	}`)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := runWithDependencies(
		[]string{"apply", planPath, "--dry-run"},
		&stdout,
		&stderr,
		runDependencies{ClientFactory: func(_ string) guildChannelsClient {
			t.Fatal("dry-run must not create a Discord client or make POST/PATCH requests")
			return nil
		}},
	)

	if exitCode != 0 {
		t.Fatalf("run apply --dry-run exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertContainsInOrder(t, stdout.String(), []string{
		"Dry run for guild g1: 5 operation(s)",
		"+ create category Projects at position 0",
		"+ create text channel alpha under cat1 at position 1",
		"~ rename channel old1 to new-name",
		"~ move channel move1 under cat2",
		"~ set position of channel pos1 to 4",
	})
}

func TestRunApplyWithoutYesDefaultsToDryRun(t *testing.T) {
	planPath := writePlanFile(t, `{
		"guild_id": "g1",
		"operations": [
			{"op": "rename_channel", "channel_id": "old1", "name": "new-name"}
		]
	}`)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := runWithDependencies(
		[]string{"apply", planPath},
		&stdout,
		&stderr,
		runDependencies{ClientFactory: func(_ string) guildChannelsClient {
			t.Fatal("apply without --yes must not create a Discord client")
			return nil
		}},
	)

	if exitCode != 0 {
		t.Fatalf("run apply without --yes exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertContainsInOrder(t, stdout.String(), []string{
		"Dry run for guild g1: 1 operation(s)",
		"~ rename channel old1 to new-name",
	})
}

func TestRunApplyYesWritesPreApplySnapshotThenAppliesSupportedOperations(t *testing.T) {
	withoutDiscordBotToken(t)
	token := "apply-token-secret"
	envFile := writeTestEnvFile(t, map[string]string{discordBotTokenEnv: token})
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	plan := `{
		"guild_id": "g1",
		"operations": [
			{"op": "create_category", "name": "Projects", "position": 0},
			{"op": "create_text_channel", "name": "alpha", "parent_id": "cat-existing", "position": 1},
			{"op": "rename_channel", "channel_id": "old1", "name": "new-name"},
			{"op": "move_channel", "channel_id": "move1", "parent_id": "cat2"},
			{"op": "set_position", "channel_id": "pos1", "position": 4}
		]
	}`
	if err := os.WriteFile(planPath, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	parentID := "cat-existing"
	initialChannels := []DiscordChannel{
		{ID: "cat-existing", Name: "Existing", Type: ChannelTypeGuildCategory, Position: 0},
		{ID: "old1", Name: "old-name", Type: ChannelTypeGuildText, ParentID: &parentID, Position: 1},
	}
	type capturedRequest struct {
		Method string
		Path   string
		Body   map[string]any
	}
	requests := []capturedRequest{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bot "+token; got != want {
			t.Errorf("Authorization header = %q, want %q", got, want)
		}
		captured := capturedRequest{Method: r.Method, Path: r.URL.Path}
		if r.Method != http.MethodGet {
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				t.Errorf("Content-Type header = %q, want application/json", r.Header.Get("Content-Type"))
			}
			if err := json.NewDecoder(r.Body).Decode(&captured.Body); err != nil {
				t.Errorf("decode %s %s body: %v", r.Method, r.URL.Path, err)
			}
		}
		requests = append(requests, captured)
		w.Header().Set("Content-Type", "application/json")

		switch r.Method + " " + r.URL.Path {
		case "GET /guilds/g1/channels":
			json.NewEncoder(w).Encode(initialChannels)
		case "POST /guilds/g1/channels":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(DiscordChannel{ID: "created", Name: fmt.Sprint(captured.Body["name"]), Type: ChannelType(captured.Body["type"].(float64))})
		case "PATCH /channels/old1", "PATCH /channels/move1", "PATCH /channels/pos1":
			json.NewEncoder(w).Encode(DiscordChannel{ID: strings.TrimPrefix(r.URL.Path, "/channels/"), Name: "updated", Type: ChannelTypeGuildText})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := runWithDependencies(
		[]string{"--env-file", envFile, "apply", planPath, "--yes"},
		&stdout,
		&stderr,
		runDependencies{ClientFactory: testClientFactory(server, token)},
	)

	if exitCode != 0 {
		t.Fatalf("run apply --yes exit code = %d, want 0; stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	wantOrder := []string{
		"GET /guilds/g1/channels",
		"POST /guilds/g1/channels",
		"POST /guilds/g1/channels",
		"PATCH /channels/old1",
		"PATCH /channels/move1",
		"PATCH /channels/pos1",
	}
	var gotOrder []string
	for _, request := range requests {
		gotOrder = append(gotOrder, request.Method+" "+request.Path)
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatalf("requests = %v, want %v", gotOrder, wantOrder)
	}
	wantBodies := []map[string]any{
		{"name": "Projects", "type": float64(4), "position": float64(0)},
		{"name": "alpha", "type": float64(0), "parent_id": "cat-existing", "position": float64(1)},
		{"name": "new-name"},
		{"parent_id": "cat2"},
		{"position": float64(4)},
	}
	var gotBodies []map[string]any
	for _, request := range requests[1:] {
		gotBodies = append(gotBodies, request.Body)
	}
	if !reflect.DeepEqual(gotBodies, wantBodies) {
		t.Fatalf("request bodies = %#v, want %#v", gotBodies, wantBodies)
	}

	snapshotPath := filepath.Join(dir, "plan.pre-apply-snapshot.json")
	data, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("read pre-apply snapshot: %v", err)
	}
	var snapshot guildSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("pre-apply snapshot is not valid JSON: %v; content=%s", err, data)
	}
	if snapshot.GuildID != "g1" || len(snapshot.Channels) != len(initialChannels) {
		t.Fatalf("snapshot = %+v, want guild g1 with %d channels", snapshot, len(initialChannels))
	}
	assertContainsInOrder(t, stdout.String(), []string{
		"Applying plan for guild g1: 5 operation(s)",
		"wrote pre-apply snapshot to " + snapshotPath,
		"applied 5 operation(s)",
	})
	if strings.Contains(stdout.String(), token) || strings.Contains(stderr.String(), token) || strings.Contains(string(data), token) {
		t.Fatalf("apply output leaks token; stdout=%q stderr=%q snapshot=%s", stdout.String(), stderr.String(), data)
	}
}

func TestRunApplyYesCreatesRolePermissionCategoryAndMovesWithAliasSync(t *testing.T) {
	withoutDiscordBotToken(t)
	token := "apply-token-secret"
	envFile := writeTestEnvFile(t, map[string]string{discordBotTokenEnv: token})
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	plan := `{
		"guild_id": "g1",
		"operations": [
			{
				"op": "create_category",
				"name": "ai-openclaw",
				"alias": "ai-openclaw",
				"permission_overwrites": [
					{"role_name": "openclaw", "allow": "123", "deny": "0"}
				]
			},
			{"op": "move_channel", "channel_id": "openclaw-channel", "parent_alias": "ai-openclaw", "sync_permissions": true}
		]
	}`
	if err := os.WriteFile(planPath, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}

	type capturedRequest struct {
		Method string
		Path   string
		Body   map[string]any
	}
	requests := []capturedRequest{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Authorization"), "Bot "+token; got != want {
			t.Errorf("Authorization header = %q, want %q", got, want)
		}
		captured := capturedRequest{Method: r.Method, Path: r.URL.Path}
		if r.Method != http.MethodGet {
			if err := json.NewDecoder(r.Body).Decode(&captured.Body); err != nil {
				t.Errorf("decode %s %s body: %v", r.Method, r.URL.Path, err)
			}
		}
		requests = append(requests, captured)
		w.Header().Set("Content-Type", "application/json")

		switch r.Method + " " + r.URL.Path {
		case "GET /guilds/g1/channels":
			json.NewEncoder(w).Encode([]DiscordChannel{{ID: "openclaw-channel", Name: "openclaw", Type: ChannelTypeGuildText}})
		case "GET /guilds/g1/roles":
			json.NewEncoder(w).Encode([]DiscordRole{{ID: "role-openclaw", Name: "openclaw"}})
		case "POST /guilds/g1/channels":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(DiscordChannel{ID: "created-ai-openclaw", Name: "ai-openclaw", Type: ChannelTypeGuildCategory})
		case "PATCH /channels/openclaw-channel":
			json.NewEncoder(w).Encode(DiscordChannel{ID: "openclaw-channel", Name: "openclaw", Type: ChannelTypeGuildText})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := runWithDependencies(
		[]string{"--env-file", envFile, "apply", planPath, "--yes"},
		&stdout,
		&stderr,
		runDependencies{ClientFactory: testClientFactory(server, token)},
	)

	if exitCode != 0 {
		t.Fatalf("run apply --yes exit code = %d, want 0; stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	wantOrder := []string{
		"GET /guilds/g1/channels",
		"GET /guilds/g1/roles",
		"POST /guilds/g1/channels",
		"PATCH /channels/openclaw-channel",
	}
	var gotOrder []string
	for _, request := range requests {
		gotOrder = append(gotOrder, request.Method+" "+request.Path)
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatalf("requests = %v, want %v", gotOrder, wantOrder)
	}
	wantBodies := []map[string]any{
		{
			"name": "ai-openclaw",
			"type": float64(4),
			"permission_overwrites": []any{
				map[string]any{"id": "role-openclaw", "type": float64(0), "allow": "123", "deny": "0"},
			},
		},
		{"parent_id": "created-ai-openclaw", "lock_permissions": true},
	}
	var gotBodies []map[string]any
	for _, request := range requests[2:] {
		gotBodies = append(gotBodies, request.Body)
	}
	if !reflect.DeepEqual(gotBodies, wantBodies) {
		t.Fatalf("request bodies = %#v, want %#v", gotBodies, wantBodies)
	}
	assertContainsInOrder(t, stdout.String(), []string{
		"Applying plan for guild g1: 2 operation(s)",
		"+ create category ai-openclaw as alias ai-openclaw with permission overwrites [@openclaw allow=123 deny=0]",
		"~ move channel openclaw-channel under alias ai-openclaw and sync permissions",
		"applied 2 operation(s)",
	})
	if strings.Contains(stdout.String(), token) || strings.Contains(stderr.String(), token) {
		t.Fatalf("apply output leaks token; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunApplyDryRunRejectsUnsupportedOperations(t *testing.T) {
	for _, unsupportedOperation := range []string{"delete_channel", "set_permission", "create_role", "dance_channel"} {
		t.Run(unsupportedOperation, func(t *testing.T) {
			planPath := writePlanFile(t, `{
				"guild_id": "g1",
				"operations": [
					{"op": "`+unsupportedOperation+`", "channel_id": "c1", "name": "unsafe"}
				]
			}`)

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := runWithDependencies(
				[]string{"apply", planPath, "--dry-run"},
				&stdout,
				&stderr,
				runDependencies{ClientFactory: func(_ string) guildChannelsClient {
					t.Fatal("invalid dry-run plan must not create a Discord client")
					return nil
				}},
			)

			if exitCode != 2 {
				t.Fatalf("run apply invalid operation exit code = %d, want 2; stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if !strings.Contains(stderr.String(), "unsupported operation") || !strings.Contains(stderr.String(), unsupportedOperation) {
				t.Fatalf("stderr %q missing unsupported operation %q", stderr.String(), unsupportedOperation)
			}
		})
	}
}

func TestRunApplyDryRunRejectsInvalidSupportedOperationFields(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "missing guild id",
			content: `{"operations":[{"op":"create_category","name":"Projects"}]}`,
			want:    "guild_id is required",
		},
		{
			name:    "missing channel id",
			content: `{"guild_id":"g1","operations":[{"op":"rename_channel","name":"renamed"}]}`,
			want:    "channel_id is required",
		},
		{
			name:    "missing position",
			content: `{"guild_id":"g1","operations":[{"op":"set_position","channel_id":"c1"}]}`,
			want:    "position is required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			planPath := writePlanFile(t, tc.content)

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			exitCode := runWithDependencies(
				[]string{"apply", planPath, "--dry-run"},
				&stdout,
				&stderr,
				runDependencies{ClientFactory: func(_ string) guildChannelsClient {
					t.Fatal("invalid dry-run plan must not create a Discord client")
					return nil
				}},
			)

			if exitCode != 2 {
				t.Fatalf("run apply invalid plan exit code = %d, want 2; stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr %q missing %q", stderr.String(), tc.want)
			}
		})
	}
}

func writePlanFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
