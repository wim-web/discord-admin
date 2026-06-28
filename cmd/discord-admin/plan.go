package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type channelPlan struct {
	GuildID    string                 `json:"guild_id"`
	Operations []channelPlanOperation `json:"operations"`
}

type channelPlanOperation struct {
	Op        string  `json:"op"`
	Name      string  `json:"name,omitempty"`
	ChannelID string  `json:"channel_id,omitempty"`
	ParentID  *string `json:"parent_id,omitempty"`
	Position  *int    `json:"position,omitempty"`
}

const (
	operationCreateCategory    = "create_category"
	operationCreateTextChannel = "create_text_channel"
	operationRenameChannel     = "rename_channel"
	operationMoveChannel       = "move_channel"
	operationSetPosition       = "set_position"
)

func runPlanInitCommand(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintf(stderr, "plan init does not accept arguments\n")
		return 2
	}

	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(sampleChannelPlan()); err != nil {
		fmt.Fprintf(stderr, "write sample plan: %v\n", err)
		return 1
	}
	return 0
}

func sampleChannelPlan() channelPlan {
	categoryPosition := 0
	textParentID := "replace-with-category-id"
	textPosition := 1
	moveParentID := "replace-with-target-category-id"
	setPosition := 2
	return channelPlan{
		GuildID: "replace-with-guild-id",
		Operations: []channelPlanOperation{
			{Op: operationCreateCategory, Name: "Projects", Position: &categoryPosition},
			{Op: operationCreateTextChannel, Name: "project-alpha", ParentID: &textParentID, Position: &textPosition},
			{Op: operationRenameChannel, ChannelID: "replace-with-channel-id", Name: "new-channel-name"},
			{Op: operationMoveChannel, ChannelID: "replace-with-channel-id", ParentID: &moveParentID},
			{Op: operationSetPosition, ChannelID: "replace-with-channel-id", Position: &setPosition},
		},
	}
}

type applyOptions struct {
	PlanPath string
	DryRun   bool
	Yes      bool
}

func runApplyCommand(args []string, globalOptions cliOptions, stdout io.Writer, stderr io.Writer, dependencies runDependencies) int {
	options, err := parseApplyOptions(args)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 2
	}

	plan, err := readChannelPlan(options.PlanPath)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 2
	}
	if err := validateChannelPlan(plan); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 2
	}

	if options.DryRun {
		printPlanDiff(stdout, plan)
		return 0
	}

	printApplyDiff(stdout, plan)

	auth, err := loadAuth(globalOptions.Auth)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 2
	}
	client, err := dependencies.newApplyClient(auth.BotToken)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	ctx := context.Background()
	channels, err := client.GetGuildChannels(ctx, plan.GuildID)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	snapshotPath := preApplySnapshotPath(options.PlanPath)
	if err := writeGuildSnapshot(snapshotPath, plan.GuildID, channels); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote pre-apply snapshot to %s\n", snapshotPath)

	if err := applyChannelPlan(ctx, client, plan); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "applied %d operation(s)\n", len(plan.Operations))
	return 0
}

func preApplySnapshotPath(planPath string) string {
	extension := filepath.Ext(planPath)
	baseName := strings.TrimSuffix(filepath.Base(planPath), extension)
	if baseName == "" {
		baseName = "plan"
	}
	return filepath.Join(filepath.Dir(planPath), baseName+".pre-apply-snapshot.json")
}

func applyChannelPlan(ctx context.Context, client channelApplyClient, plan channelPlan) error {
	for index, operation := range plan.Operations {
		if err := applyChannelPlanOperation(ctx, client, plan.GuildID, operation); err != nil {
			return fmt.Errorf("apply operation %d (%s): %w", index+1, operation.Op, err)
		}
	}
	return nil
}

func applyChannelPlanOperation(ctx context.Context, client channelApplyClient, guildID string, operation channelPlanOperation) error {
	switch operation.Op {
	case operationCreateCategory:
		_, err := client.CreateGuildChannel(ctx, guildID, CreateGuildChannelRequest{
			Name:     operation.Name,
			Type:     ChannelTypeGuildCategory,
			Position: operation.Position,
		})
		return err
	case operationCreateTextChannel:
		_, err := client.CreateGuildChannel(ctx, guildID, CreateGuildChannelRequest{
			Name:     operation.Name,
			Type:     ChannelTypeGuildText,
			ParentID: operation.ParentID,
			Position: operation.Position,
		})
		return err
	case operationRenameChannel:
		_, err := client.UpdateChannel(ctx, operation.ChannelID, UpdateChannelRequest{Name: &operation.Name})
		return err
	case operationMoveChannel:
		_, err := client.UpdateChannel(ctx, operation.ChannelID, UpdateChannelRequest{ParentID: operation.ParentID})
		return err
	case operationSetPosition:
		_, err := client.UpdateChannel(ctx, operation.ChannelID, UpdateChannelRequest{Position: operation.Position})
		return err
	default:
		return unsupportedOperationError(operation.Op)
	}
}

func parseApplyOptions(args []string) (applyOptions, error) {
	var options applyOptions
	for _, arg := range args {
		switch arg {
		case "--dry-run":
			options.DryRun = true
		case "--yes":
			options.Yes = true
		default:
			if strings.HasPrefix(arg, "-") {
				return applyOptions{}, fmt.Errorf("unknown apply option %q", arg)
			}
			if options.PlanPath != "" {
				return applyOptions{}, fmt.Errorf("apply accepts exactly one plan file")
			}
			options.PlanPath = arg
		}
	}
	if options.PlanPath == "" {
		return applyOptions{}, fmt.Errorf("apply requires <plan.json>")
	}
	if options.DryRun && options.Yes {
		return applyOptions{}, fmt.Errorf("apply accepts either --dry-run or --yes, not both")
	}
	if !options.Yes {
		options.DryRun = true
	}
	return options, nil
}

func readChannelPlan(path string) (channelPlan, error) {
	file, err := os.Open(path)
	if err != nil {
		return channelPlan{}, fmt.Errorf("read plan %s: %w", path, err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var plan channelPlan
	if err := decoder.Decode(&plan); err != nil {
		return channelPlan{}, fmt.Errorf("parse plan %s: %w", path, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return channelPlan{}, fmt.Errorf("parse plan %s: contains multiple JSON values", path)
		}
		return channelPlan{}, fmt.Errorf("parse plan %s: %w", path, err)
	}
	return plan, nil
}

func validateChannelPlan(plan channelPlan) error {
	if strings.TrimSpace(plan.GuildID) == "" {
		return fmt.Errorf("plan validation failed: guild_id is required")
	}
	if len(plan.Operations) == 0 {
		return fmt.Errorf("plan validation failed: operations must contain at least one operation")
	}
	for index, operation := range plan.Operations {
		if err := validateChannelPlanOperation(operation); err != nil {
			return fmt.Errorf("plan validation failed: operation %d: %w", index+1, err)
		}
	}
	return nil
}

func validateChannelPlanOperation(operation channelPlanOperation) error {
	switch operation.Op {
	case operationCreateCategory:
		if err := requireName(operation); err != nil {
			return err
		}
		return validateOptionalPosition(operation)
	case operationCreateTextChannel:
		if err := requireName(operation); err != nil {
			return err
		}
		if err := validateOptionalParentID(operation); err != nil {
			return err
		}
		return validateOptionalPosition(operation)
	case operationRenameChannel:
		if err := requireChannelID(operation); err != nil {
			return err
		}
		return requireName(operation)
	case operationMoveChannel:
		if err := requireChannelID(operation); err != nil {
			return err
		}
		return requireParentID(operation)
	case operationSetPosition:
		if err := requireChannelID(operation); err != nil {
			return err
		}
		return requirePosition(operation)
	default:
		return unsupportedOperationError(operation.Op)
	}
}

func requireName(operation channelPlanOperation) error {
	if strings.TrimSpace(operation.Name) == "" {
		return fmt.Errorf("name is required")
	}
	return nil
}

func requireChannelID(operation channelPlanOperation) error {
	if strings.TrimSpace(operation.ChannelID) == "" {
		return fmt.Errorf("channel_id is required")
	}
	return nil
}

func requireParentID(operation channelPlanOperation) error {
	if operation.ParentID == nil || strings.TrimSpace(*operation.ParentID) == "" {
		return fmt.Errorf("parent_id is required")
	}
	return nil
}

func validateOptionalParentID(operation channelPlanOperation) error {
	if operation.ParentID != nil && strings.TrimSpace(*operation.ParentID) == "" {
		return fmt.Errorf("parent_id must not be empty when provided")
	}
	return nil
}

func requirePosition(operation channelPlanOperation) error {
	if operation.Position == nil {
		return fmt.Errorf("position is required")
	}
	if *operation.Position < 0 {
		return fmt.Errorf("position must be >= 0")
	}
	return nil
}

func validateOptionalPosition(operation channelPlanOperation) error {
	if operation.Position != nil && *operation.Position < 0 {
		return fmt.Errorf("position must be >= 0")
	}
	return nil
}

func unsupportedOperationError(operation string) error {
	operation = strings.TrimSpace(operation)
	if operation == "" {
		return fmt.Errorf("unsupported operation: op is required")
	}
	return fmt.Errorf("unsupported operation %q; delete, permission, role, and unknown operations are not supported", operation)
}

func printPlanDiff(w io.Writer, plan channelPlan) {
	printOperationDiff(w, "Dry run", plan)
}

func printApplyDiff(w io.Writer, plan channelPlan) {
	printOperationDiff(w, "Applying plan", plan)
}

func printOperationDiff(w io.Writer, label string, plan channelPlan) {
	fmt.Fprintf(w, "%s for guild %s: %d operation(s)\n", label, plan.GuildID, len(plan.Operations))
	for _, operation := range plan.Operations {
		switch operation.Op {
		case operationCreateCategory:
			if operation.Position != nil {
				fmt.Fprintf(w, "+ create category %s at position %d\n", operation.Name, *operation.Position)
			} else {
				fmt.Fprintf(w, "+ create category %s\n", operation.Name)
			}
		case operationCreateTextChannel:
			line := fmt.Sprintf("+ create text channel %s", operation.Name)
			if operation.ParentID != nil {
				line += fmt.Sprintf(" under %s", *operation.ParentID)
			}
			if operation.Position != nil {
				line += fmt.Sprintf(" at position %d", *operation.Position)
			}
			fmt.Fprintln(w, line)
		case operationRenameChannel:
			fmt.Fprintf(w, "~ rename channel %s to %s\n", operation.ChannelID, operation.Name)
		case operationMoveChannel:
			fmt.Fprintf(w, "~ move channel %s under %s\n", operation.ChannelID, *operation.ParentID)
		case operationSetPosition:
			fmt.Fprintf(w, "~ set position of channel %s to %d\n", operation.ChannelID, *operation.Position)
		}
	}
}
