package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

const usage = `Usage: discord-admin <command> [options]

Manage Discord server channel organization through the Discord REST API.

Commands:
  list --guild-id <id>         List guild categories and channels.
  snapshot --guild-id <id> --out <file>
                               Write the current guild channel state to JSON.
  plan init                    Create a sample safe channel-organization plan.
  apply <plan.json> --dry-run  Validate and print planned changes without writes.
  apply <plan.json> --yes      Apply an already-reviewed plan.

Global options:
  -h, --help                   Show this help text.
  --env-file <path>            Load DISCORD_BOT_TOKEN from a dotenv-style file.

Environment:
  DISCORD_BOT_TOKEN            Discord bot token used for API requests.
  DISCORD_GUILD_ID             Optional explicit default guild id for read-only commands.

Safety:
  Real Discord write operations will require explicit command support and confirmation.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout io.Writer, stderr io.Writer) int {
	return runWithDependencies(args, stdout, stderr, runDependencies{})
}

type guildChannelsClient interface {
	GetGuildChannels(ctx context.Context, guildID string) ([]DiscordChannel, error)
}

type channelApplyClient interface {
	guildChannelsClient
	CreateGuildChannel(ctx context.Context, guildID string, request CreateGuildChannelRequest) (DiscordChannel, error)
	UpdateChannel(ctx context.Context, channelID string, request UpdateChannelRequest) (DiscordChannel, error)
}

type runDependencies struct {
	ClientFactory func(botToken string) guildChannelsClient
}

func (dependencies runDependencies) newClient(botToken string) guildChannelsClient {
	if dependencies.ClientFactory != nil {
		return dependencies.ClientFactory(botToken)
	}
	return NewDiscordClient(botToken)
}

func (dependencies runDependencies) newApplyClient(botToken string) (channelApplyClient, error) {
	client := dependencies.newClient(botToken)
	applyClient, ok := client.(channelApplyClient)
	if !ok {
		return nil, fmt.Errorf("configured Discord client does not support apply operations")
	}
	return applyClient, nil
}

func runWithDependencies(args []string, stdout io.Writer, stderr io.Writer, dependencies runDependencies) int {
	options, commandArgs, err := parseGlobalOptions(args)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n\n", err)
		printUsage(stderr)
		return 2
	}

	if len(commandArgs) == 0 || commandArgs[0] == "-h" || commandArgs[0] == "--help" || commandArgs[0] == "help" {
		printUsage(stdout)
		return 0
	}

	switch commandArgs[0] {
	case "list":
		return runListCommand(commandArgs[1:], options, stdout, stderr, dependencies)
	case "snapshot":
		return runSnapshotCommand(commandArgs[1:], options, stdout, stderr, dependencies)
	case "plan":
		if len(commandArgs) > 1 && commandArgs[1] == "init" {
			return runPlanInitCommand(commandArgs[2:], stdout, stderr)
		}
		fmt.Fprintf(stderr, "unknown plan subcommand\n\n")
		printUsage(stderr)
		return 2
	case "apply":
		return runApplyCommand(commandArgs[1:], options, stdout, stderr, dependencies)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", commandArgs[0])
		printUsage(stderr)
		return 2
	}
}

type cliOptions struct {
	Auth AuthOptions
}

func parseGlobalOptions(args []string) (cliOptions, []string, error) {
	var options cliOptions
	commandArgs := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--env-file":
			if i+1 >= len(args) {
				return cliOptions{}, nil, fmt.Errorf("--env-file requires a path")
			}
			options.Auth.EnvFile = args[i+1]
			i++
		case strings.HasPrefix(arg, "--env-file="):
			path := strings.TrimPrefix(arg, "--env-file=")
			if path == "" {
				return cliOptions{}, nil, fmt.Errorf("--env-file requires a path")
			}
			options.Auth.EnvFile = path
		default:
			commandArgs = append(commandArgs, arg)
		}
	}

	return options, commandArgs, nil
}

const guildIDEnv = "DISCORD_GUILD_ID"

type channelDiscoveryOptions struct {
	GuildID string
	OutFile string
}

func runListCommand(args []string, options cliOptions, stdout io.Writer, stderr io.Writer, dependencies runDependencies) int {
	commandOptions, err := parseChannelDiscoveryOptions("list", args)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 2
	}
	guildID, err := resolveGuildID(options.Auth, commandOptions.GuildID)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 2
	}
	channels, err := fetchGuildChannels(guildID, options, dependencies)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	printChannelTree(stdout, guildID, channels)
	return 0
}

func runSnapshotCommand(args []string, options cliOptions, stdout io.Writer, stderr io.Writer, dependencies runDependencies) int {
	commandOptions, err := parseChannelDiscoveryOptions("snapshot", args)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 2
	}
	if commandOptions.OutFile == "" {
		fmt.Fprintf(stderr, "snapshot requires --out <file>\n")
		return 2
	}
	guildID, err := resolveGuildID(options.Auth, commandOptions.GuildID)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 2
	}
	channels, err := fetchGuildChannels(guildID, options, dependencies)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	if err := writeGuildSnapshot(commandOptions.OutFile, guildID, channels); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote snapshot to %s\n", commandOptions.OutFile)
	return 0
}

func parseChannelDiscoveryOptions(command string, args []string) (channelDiscoveryOptions, error) {
	var options channelDiscoveryOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--guild-id":
			if i+1 >= len(args) || args[i+1] == "" {
				return channelDiscoveryOptions{}, fmt.Errorf("%s requires --guild-id <id>", command)
			}
			options.GuildID = args[i+1]
			i++
		case strings.HasPrefix(arg, "--guild-id="):
			options.GuildID = strings.TrimPrefix(arg, "--guild-id=")
			if options.GuildID == "" {
				return channelDiscoveryOptions{}, fmt.Errorf("%s requires --guild-id <id>", command)
			}
		case command == "snapshot" && arg == "--out":
			if i+1 >= len(args) || args[i+1] == "" {
				return channelDiscoveryOptions{}, fmt.Errorf("snapshot requires --out <file>")
			}
			options.OutFile = args[i+1]
			i++
		case command == "snapshot" && strings.HasPrefix(arg, "--out="):
			options.OutFile = strings.TrimPrefix(arg, "--out=")
			if options.OutFile == "" {
				return channelDiscoveryOptions{}, fmt.Errorf("snapshot requires --out <file>")
			}
		default:
			return channelDiscoveryOptions{}, fmt.Errorf("unknown %s option %q", command, arg)
		}
	}
	return options, nil
}

func resolveGuildID(authOptions AuthOptions, explicitGuildID string) (string, error) {
	if guildID := strings.TrimSpace(explicitGuildID); guildID != "" {
		return guildID, nil
	}
	if guildID := strings.TrimSpace(os.Getenv(guildIDEnv)); guildID != "" {
		return guildID, nil
	}
	if authOptions.EnvFile != "" {
		values, err := readEnvFile(authOptions.EnvFile)
		if err != nil {
			return "", err
		}
		if guildID := strings.TrimSpace(values[guildIDEnv]); guildID != "" {
			return guildID, nil
		}
	}
	return "", fmt.Errorf("--guild-id <id> is required unless %s is configured", guildIDEnv)
}

func fetchGuildChannels(guildID string, options cliOptions, dependencies runDependencies) ([]DiscordChannel, error) {
	auth, err := loadAuth(options.Auth)
	if err != nil {
		return nil, err
	}
	return dependencies.newClient(auth.BotToken).GetGuildChannels(context.Background(), guildID)
}

func printChannelTree(w io.Writer, guildID string, channels []DiscordChannel) {
	channels = sortedChannels(channels)
	fmt.Fprintf(w, "Guild %s\n", guildID)

	childrenByParent := make(map[string][]DiscordChannel)
	categoryIDs := make(map[string]bool)
	for _, channel := range channels {
		if channel.Type == ChannelTypeGuildCategory {
			categoryIDs[channel.ID] = true
		}
	}

	var categories []DiscordChannel
	var uncategorized []DiscordChannel
	for _, channel := range channels {
		if channel.Type == ChannelTypeGuildCategory {
			categories = append(categories, channel)
			continue
		}
		if channel.ParentID != nil && categoryIDs[*channel.ParentID] {
			childrenByParent[*channel.ParentID] = append(childrenByParent[*channel.ParentID], channel)
			continue
		}
		uncategorized = append(uncategorized, channel)
	}

	for _, category := range categories {
		fmt.Fprintf(w, "%s %s (%s)\n", channelTypeLabel(category.Type), category.Name, category.ID)
		for _, child := range sortedChannels(childrenByParent[category.ID]) {
			fmt.Fprintf(w, "  %s %s (%s)\n", channelTypeLabel(child.Type), child.Name, child.ID)
		}
	}
	for _, channel := range uncategorized {
		fmt.Fprintf(w, "%s %s (%s)\n", channelTypeLabel(channel.Type), channel.Name, channel.ID)
	}
}

type guildSnapshot struct {
	GuildID  string            `json:"guild_id"`
	Channels []snapshotChannel `json:"channels"`
}

type snapshotChannel struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Type     ChannelType `json:"type"`
	ParentID *string     `json:"parent_id"`
	Position int         `json:"position"`
}

func writeGuildSnapshot(path string, guildID string, channels []DiscordChannel) error {
	snapshot := guildSnapshot{GuildID: guildID}
	for _, channel := range sortedChannels(channels) {
		snapshot.Channels = append(snapshot.Channels, snapshotChannel{
			ID:       channel.ID,
			Name:     channel.Name,
			Type:     channel.Type,
			ParentID: channel.ParentID,
			Position: channel.Position,
		})
	}

	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create snapshot %s: %w", path, err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(snapshot); err != nil {
		_ = file.Close()
		return fmt.Errorf("write snapshot %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close snapshot %s: %w", path, err)
	}
	return nil
}

func sortedChannels(channels []DiscordChannel) []DiscordChannel {
	sorted := append([]DiscordChannel(nil), channels...)
	sort.SliceStable(sorted, func(i int, j int) bool {
		if sorted[i].Position != sorted[j].Position {
			return sorted[i].Position < sorted[j].Position
		}
		if sorted[i].Type != sorted[j].Type {
			return sorted[i].Type < sorted[j].Type
		}
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].ID < sorted[j].ID
	})
	return sorted
}

func channelTypeLabel(channelType ChannelType) string {
	switch channelType {
	case ChannelTypeGuildCategory:
		return "[category]"
	case ChannelTypeGuildText:
		return "[text]"
	case ChannelTypeGuildVoice:
		return "[voice]"
	default:
		return fmt.Sprintf("[type %d]", channelType)
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, usage)
}

func notImplemented(command string, stderr io.Writer) int {
	fmt.Fprintf(stderr, "%s is not implemented yet. Run discord-admin --help for available command syntax.\n", command)
	return 2
}
