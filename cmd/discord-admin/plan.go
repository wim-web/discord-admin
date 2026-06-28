package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type channelPlan struct {
	GuildID    string                 `json:"guild_id"`
	Operations []channelPlanOperation `json:"operations"`
}

type channelPlanOperation struct {
	Op                           string                            `json:"op"`
	Name                         string                            `json:"name,omitempty"`
	ChannelID                    string                            `json:"channel_id,omitempty"`
	Alias                        string                            `json:"alias,omitempty"`
	ParentID                     *string                           `json:"parent_id,omitempty"`
	ParentAlias                  string                            `json:"parent_alias,omitempty"`
	Position                     *int                              `json:"position,omitempty"`
	SyncPermissions              *bool                             `json:"sync_permissions,omitempty"`
	PermissionOverwrites         []channelPlanPermissionOverwrite  `json:"permission_overwrites,omitempty"`
	ExpectedPermissionOverwrites *[]channelPlanPermissionOverwrite `json:"expected_permission_overwrites,omitempty"`
}

type channelPlanPermissionOverwrite struct {
	RoleName string `json:"role_name,omitempty"`
	RoleID   string `json:"role_id,omitempty"`
	Type     string `json:"type,omitempty"`
	Allow    string `json:"allow,omitempty"`
	Deny     string `json:"deny,omitempty"`
}

const (
	operationCreateCategory          = "create_category"
	operationCreateTextChannel       = "create_text_channel"
	operationRenameChannel           = "rename_channel"
	operationMoveChannel             = "move_channel"
	operationSetPosition             = "set_position"
	operationSetPermissionOverwrites = "set_permission_overwrites"
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
	moveParentAlias := "projects"
	setPosition := 2
	syncPermissions := true
	return channelPlan{
		GuildID: "replace-with-guild-id",
		Operations: []channelPlanOperation{
			{
				Op:       operationCreateCategory,
				Name:     "Projects",
				Alias:    "projects",
				Position: &categoryPosition,
				PermissionOverwrites: []channelPlanPermissionOverwrite{
					{RoleName: "project-bot", Allow: "0", Deny: "0"},
				},
			},
			{Op: operationCreateTextChannel, Name: "project-alpha", ParentID: &textParentID, Position: &textPosition},
			{Op: operationRenameChannel, ChannelID: "replace-with-channel-id", Name: "new-channel-name"},
			{Op: operationMoveChannel, ChannelID: "replace-with-channel-id", ParentAlias: moveParentAlias, SyncPermissions: &syncPermissions},
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

	if err := applyChannelPlan(ctx, client, plan, channels); err != nil {
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

type planApplyState struct {
	aliases       map[string]string
	roleIDsByName map[string]string
	channelsByID  map[string]DiscordChannel
}

func applyChannelPlan(ctx context.Context, client channelApplyClient, plan channelPlan, preApplyChannels []DiscordChannel) error {
	state, err := newPlanApplyState(ctx, client, plan, preApplyChannels)
	if err != nil {
		return err
	}
	for index, operation := range plan.Operations {
		if err := applyChannelPlanOperation(ctx, client, plan.GuildID, operation, state); err != nil {
			return fmt.Errorf("apply operation %d (%s): %w", index+1, operation.Op, err)
		}
	}
	return nil
}

func newPlanApplyState(ctx context.Context, client channelApplyClient, plan channelPlan, preApplyChannels []DiscordChannel) (*planApplyState, error) {
	state := &planApplyState{aliases: map[string]string{}, roleIDsByName: map[string]string{}, channelsByID: map[string]DiscordChannel{}}
	for _, channel := range preApplyChannels {
		state.channelsByID[channel.ID] = channel
	}
	if !planUsesRoleNames(plan) {
		return state, nil
	}
	roles, err := client.GetGuildRoles(ctx, plan.GuildID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, role := range roles {
		name := strings.TrimSpace(role.Name)
		if name == "" {
			continue
		}
		if seen[name] {
			delete(state.roleIDsByName, name)
			continue
		}
		seen[name] = true
		state.roleIDsByName[name] = role.ID
	}
	return state, nil
}

func planUsesRoleNames(plan channelPlan) bool {
	for _, operation := range plan.Operations {
		for _, overwrite := range operation.PermissionOverwrites {
			if strings.TrimSpace(overwrite.RoleName) != "" {
				return true
			}
		}
		if operation.ExpectedPermissionOverwrites != nil {
			for _, overwrite := range *operation.ExpectedPermissionOverwrites {
				if strings.TrimSpace(overwrite.RoleName) != "" {
					return true
				}
			}
		}
	}
	return false
}

func applyChannelPlanOperation(ctx context.Context, client channelApplyClient, guildID string, operation channelPlanOperation, state *planApplyState) error {
	switch operation.Op {
	case operationCreateCategory:
		overwrites, err := resolvePermissionOverwrites(operation.PermissionOverwrites, state)
		if err != nil {
			return err
		}
		created, err := client.CreateGuildChannel(ctx, guildID, CreateGuildChannelRequest{
			Name:                 operation.Name,
			Type:                 ChannelTypeGuildCategory,
			Position:             operation.Position,
			PermissionOverwrites: overwrites,
		})
		if err != nil {
			return err
		}
		rememberAlias(operation, created, state)
		return nil
	case operationCreateTextChannel:
		parentID, err := resolveParentID(operation, state)
		if err != nil {
			return err
		}
		overwrites, err := resolvePermissionOverwrites(operation.PermissionOverwrites, state)
		if err != nil {
			return err
		}
		created, err := client.CreateGuildChannel(ctx, guildID, CreateGuildChannelRequest{
			Name:                 operation.Name,
			Type:                 ChannelTypeGuildText,
			ParentID:             parentID,
			Position:             operation.Position,
			PermissionOverwrites: overwrites,
		})
		if err != nil {
			return err
		}
		rememberAlias(operation, created, state)
		return nil
	case operationRenameChannel:
		_, err := client.UpdateChannel(ctx, operation.ChannelID, UpdateChannelRequest{Name: &operation.Name})
		return err
	case operationMoveChannel:
		parentID, err := resolveParentID(operation, state)
		if err != nil {
			return err
		}
		_, err = client.UpdateChannel(ctx, operation.ChannelID, UpdateChannelRequest{ParentID: parentID, LockPermissions: operation.SyncPermissions})
		return err
	case operationSetPosition:
		_, err := client.UpdateChannel(ctx, operation.ChannelID, UpdateChannelRequest{Position: operation.Position})
		return err
	case operationSetPermissionOverwrites:
		overwrites, err := resolvePermissionOverwrites(operation.PermissionOverwrites, state)
		if err != nil {
			return err
		}
		if operation.ExpectedPermissionOverwrites != nil {
			expected, err := resolvePermissionOverwrites(*operation.ExpectedPermissionOverwrites, state)
			if err != nil {
				return err
			}
			if err := verifyExpectedPermissionOverwrites(operation.ChannelID, expected, state); err != nil {
				return err
			}
		}
		_, err = client.UpdateChannel(ctx, operation.ChannelID, UpdateChannelRequest{PermissionOverwrites: &overwrites})
		return err
	default:
		return unsupportedOperationError(operation.Op)
	}
}

func verifyExpectedPermissionOverwrites(channelID string, expected []PermissionOverwrite, state *planApplyState) error {
	channelID = strings.TrimSpace(channelID)
	channel, ok := state.channelsByID[channelID]
	if !ok {
		return fmt.Errorf("expected_permission_overwrites check failed for channel %s: channel was not found in pre-apply snapshot", channelID)
	}
	if permissionOverwritesEqual(channel.PermissionOverwrites, expected) {
		return nil
	}
	return fmt.Errorf(
		"expected_permission_overwrites check failed for channel %s: current %s does not match expected %s",
		channelID,
		formatResolvedPermissionOverwrites(channel.PermissionOverwrites),
		formatResolvedPermissionOverwrites(expected),
	)
}

func permissionOverwritesEqual(left []PermissionOverwrite, right []PermissionOverwrite) bool {
	leftCanonical := canonicalPermissionOverwrites(left)
	rightCanonical := canonicalPermissionOverwrites(right)
	if len(leftCanonical) != len(rightCanonical) {
		return false
	}
	for index := range leftCanonical {
		if leftCanonical[index] != rightCanonical[index] {
			return false
		}
	}
	return true
}

func canonicalPermissionOverwrites(overwrites []PermissionOverwrite) []string {
	canonical := make([]string, 0, len(overwrites))
	for _, overwrite := range overwrites {
		canonical = append(canonical, fmt.Sprintf("%d\x00%s\x00%s\x00%s", overwrite.Type, overwrite.ID, normalizePermissionBits(overwrite.Allow), normalizePermissionBits(overwrite.Deny)))
	}
	sort.Strings(canonical)
	return canonical
}

func rememberAlias(operation channelPlanOperation, created DiscordChannel, state *planApplyState) {
	alias := strings.TrimSpace(operation.Alias)
	if alias == "" {
		return
	}
	state.aliases[alias] = created.ID
}

func resolveParentID(operation channelPlanOperation, state *planApplyState) (*string, error) {
	if operation.ParentID != nil {
		parentID := strings.TrimSpace(*operation.ParentID)
		return &parentID, nil
	}
	alias := strings.TrimSpace(operation.ParentAlias)
	if alias == "" {
		return nil, nil
	}
	parentID, ok := state.aliases[alias]
	if !ok || parentID == "" {
		return nil, fmt.Errorf("parent_alias %q has not been created earlier in the plan", alias)
	}
	return &parentID, nil
}

func resolvePermissionOverwrites(overwrites []channelPlanPermissionOverwrite, state *planApplyState) ([]PermissionOverwrite, error) {
	if len(overwrites) == 0 {
		return nil, nil
	}
	resolved := make([]PermissionOverwrite, 0, len(overwrites))
	for _, overwrite := range overwrites {
		roleID := strings.TrimSpace(overwrite.RoleID)
		roleName := strings.TrimSpace(overwrite.RoleName)
		if roleName != "" {
			var ok bool
			roleID, ok = state.roleIDsByName[roleName]
			if !ok || roleID == "" {
				return nil, fmt.Errorf("role_name %q was not found or is ambiguous", roleName)
			}
		}
		allow := normalizePermissionBits(overwrite.Allow)
		deny := normalizePermissionBits(overwrite.Deny)
		resolved = append(resolved, PermissionOverwrite{
			ID:    roleID,
			Type:  PermissionOverwriteTypeRole,
			Allow: allow,
			Deny:  deny,
		})
	}
	return resolved, nil
}

func normalizePermissionBits(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "0"
	}
	return value
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
	aliases := map[string]bool{}
	for index, operation := range plan.Operations {
		if err := validateChannelPlanOperation(operation, aliases); err != nil {
			return fmt.Errorf("plan validation failed: operation %d: %w", index+1, err)
		}
		alias := strings.TrimSpace(operation.Alias)
		if alias != "" {
			aliases[alias] = true
		}
	}
	return nil
}

func validateChannelPlanOperation(operation channelPlanOperation, aliases map[string]bool) error {
	if err := validateGenericOperationFields(operation); err != nil {
		return err
	}
	switch operation.Op {
	case operationCreateCategory:
		if err := requireName(operation); err != nil {
			return err
		}
		if err := validateCreateAlias(operation, aliases); err != nil {
			return err
		}
		if err := validatePermissionOverwrites(operation.PermissionOverwrites); err != nil {
			return err
		}
		return validateOptionalPosition(operation)
	case operationCreateTextChannel:
		if err := requireName(operation); err != nil {
			return err
		}
		if err := validateCreateAlias(operation, aliases); err != nil {
			return err
		}
		if err := validateOptionalParentReference(operation, aliases); err != nil {
			return err
		}
		if err := validatePermissionOverwrites(operation.PermissionOverwrites); err != nil {
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
		return requireParentReference(operation, aliases)
	case operationSetPosition:
		if err := requireChannelID(operation); err != nil {
			return err
		}
		return requirePosition(operation)
	case operationSetPermissionOverwrites:
		if err := requireChannelID(operation); err != nil {
			return err
		}
		if err := rejectPermissionOverwriteReplacementFields(operation); err != nil {
			return err
		}
		if len(operation.PermissionOverwrites) == 0 {
			return fmt.Errorf("permission_overwrites must contain at least one role overwrite")
		}
		if err := validatePermissionOverwrites(operation.PermissionOverwrites); err != nil {
			return err
		}
		if operation.ExpectedPermissionOverwrites != nil {
			return validatePermissionOverwrites(*operation.ExpectedPermissionOverwrites)
		}
		return nil
	default:
		return unsupportedOperationError(operation.Op)
	}
}

func validateGenericOperationFields(operation channelPlanOperation) error {
	if strings.TrimSpace(operation.Alias) != "" && operation.Op != operationCreateCategory && operation.Op != operationCreateTextChannel {
		return fmt.Errorf("alias is only supported on create_category and create_text_channel")
	}
	if len(operation.PermissionOverwrites) > 0 && operation.Op != operationCreateCategory && operation.Op != operationCreateTextChannel && operation.Op != operationSetPermissionOverwrites {
		return fmt.Errorf("permission_overwrites is only supported on create_category, create_text_channel, and set_permission_overwrites")
	}
	if operation.ExpectedPermissionOverwrites != nil && operation.Op != operationSetPermissionOverwrites {
		return fmt.Errorf("expected_permission_overwrites is only supported on set_permission_overwrites")
	}
	if operation.SyncPermissions != nil && operation.Op != operationMoveChannel {
		return fmt.Errorf("sync_permissions is only supported on move_channel")
	}
	if strings.TrimSpace(operation.ParentAlias) != "" && operation.Op != operationCreateTextChannel && operation.Op != operationMoveChannel {
		return fmt.Errorf("parent_alias is only supported on create_text_channel and move_channel")
	}
	return nil
}

func rejectPermissionOverwriteReplacementFields(operation channelPlanOperation) error {
	if strings.TrimSpace(operation.Name) != "" {
		return fmt.Errorf("name is not supported on set_permission_overwrites")
	}
	if strings.TrimSpace(operation.Alias) != "" {
		return fmt.Errorf("alias is not supported on set_permission_overwrites")
	}
	if operation.ParentID != nil || strings.TrimSpace(operation.ParentAlias) != "" {
		return fmt.Errorf("parent_id and parent_alias are not supported on set_permission_overwrites")
	}
	if operation.Position != nil {
		return fmt.Errorf("position is not supported on set_permission_overwrites")
	}
	if operation.SyncPermissions != nil {
		return fmt.Errorf("sync_permissions is not supported on set_permission_overwrites")
	}
	return nil
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

func validateCreateAlias(operation channelPlanOperation, aliases map[string]bool) error {
	alias := strings.TrimSpace(operation.Alias)
	if alias == "" {
		return nil
	}
	if aliases[alias] {
		return fmt.Errorf("alias %q is already used by an earlier create operation", alias)
	}
	return nil
}

func requireParentReference(operation channelPlanOperation, aliases map[string]bool) error {
	if operation.ParentID != nil && strings.TrimSpace(*operation.ParentID) == "" {
		return fmt.Errorf("parent_id must not be empty when provided")
	}
	if operation.ParentID != nil && strings.TrimSpace(operation.ParentAlias) != "" {
		return fmt.Errorf("provide only one of parent_id or parent_alias")
	}
	if operation.ParentID == nil && strings.TrimSpace(operation.ParentAlias) == "" {
		return fmt.Errorf("parent_id or parent_alias is required")
	}
	return validateParentAliasExists(operation, aliases)
}

func validateOptionalParentReference(operation channelPlanOperation, aliases map[string]bool) error {
	if operation.ParentID != nil && strings.TrimSpace(*operation.ParentID) == "" {
		return fmt.Errorf("parent_id must not be empty when provided")
	}
	if operation.ParentID != nil && strings.TrimSpace(operation.ParentAlias) != "" {
		return fmt.Errorf("provide only one of parent_id or parent_alias")
	}
	return validateParentAliasExists(operation, aliases)
}

func validateParentAliasExists(operation channelPlanOperation, aliases map[string]bool) error {
	alias := strings.TrimSpace(operation.ParentAlias)
	if alias == "" {
		return nil
	}
	if !aliases[alias] {
		return fmt.Errorf("parent_alias %q must reference an earlier create operation alias", alias)
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

func validatePermissionOverwrites(overwrites []channelPlanPermissionOverwrite) error {
	for index, overwrite := range overwrites {
		if err := validatePermissionOverwrite(overwrite); err != nil {
			return fmt.Errorf("permission_overwrites[%d]: %w", index, err)
		}
	}
	return nil
}

func validatePermissionOverwrite(overwrite channelPlanPermissionOverwrite) error {
	overwriteType := strings.TrimSpace(overwrite.Type)
	if overwriteType != "" && overwriteType != "role" {
		return fmt.Errorf("only role permission overwrites are supported")
	}
	roleName := strings.TrimSpace(overwrite.RoleName)
	roleID := strings.TrimSpace(overwrite.RoleID)
	if roleName == "" && roleID == "" {
		return fmt.Errorf("role_name or role_id is required")
	}
	if roleName != "" && roleID != "" {
		return fmt.Errorf("provide only one of role_name or role_id")
	}
	if err := validatePermissionBitString("allow", overwrite.Allow); err != nil {
		return err
	}
	return validatePermissionBitString("deny", overwrite.Deny)
}

func validatePermissionBitString(name string, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if _, err := strconv.ParseUint(value, 10, 64); err != nil {
		return fmt.Errorf("%s must be an unsigned decimal permission bit string", name)
	}
	return nil
}

func unsupportedOperationError(operation string) error {
	operation = strings.TrimSpace(operation)
	if operation == "" {
		return fmt.Errorf("unsupported operation: op is required")
	}
	return fmt.Errorf("unsupported operation %q; delete, partial/direct permission mutation, role, and unknown operations are not supported", operation)
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
			line := fmt.Sprintf("+ create category %s", operation.Name)
			if operation.Position != nil {
				line += fmt.Sprintf(" at position %d", *operation.Position)
			}
			line += aliasSuffix(operation)
			line += permissionOverwriteSuffix(operation)
			fmt.Fprintln(w, line)
		case operationCreateTextChannel:
			line := fmt.Sprintf("+ create text channel %s", operation.Name)
			line += parentSuffix(operation)
			if operation.Position != nil {
				line += fmt.Sprintf(" at position %d", *operation.Position)
			}
			line += aliasSuffix(operation)
			line += permissionOverwriteSuffix(operation)
			fmt.Fprintln(w, line)
		case operationRenameChannel:
			fmt.Fprintf(w, "~ rename channel %s to %s\n", operation.ChannelID, operation.Name)
		case operationMoveChannel:
			line := fmt.Sprintf("~ move channel %s", operation.ChannelID)
			line += parentSuffix(operation)
			if operation.SyncPermissions != nil && *operation.SyncPermissions {
				line += " and sync permissions"
			}
			fmt.Fprintln(w, line)
		case operationSetPosition:
			fmt.Fprintf(w, "~ set position of channel %s to %d\n", operation.ChannelID, *operation.Position)
		case operationSetPermissionOverwrites:
			line := fmt.Sprintf("~ replace permission overwrites on channel %s", operation.ChannelID)
			if operation.ExpectedPermissionOverwrites != nil {
				line += fmt.Sprintf(" from %s", formatPlanPermissionOverwrites(*operation.ExpectedPermissionOverwrites))
			}
			line += fmt.Sprintf(" to %s", formatPlanPermissionOverwrites(operation.PermissionOverwrites))
			fmt.Fprintln(w, line)
		}
	}
}

func aliasSuffix(operation channelPlanOperation) string {
	if strings.TrimSpace(operation.Alias) == "" {
		return ""
	}
	return fmt.Sprintf(" as alias %s", operation.Alias)
}

func parentSuffix(operation channelPlanOperation) string {
	if operation.ParentID != nil {
		return fmt.Sprintf(" under %s", *operation.ParentID)
	}
	if strings.TrimSpace(operation.ParentAlias) != "" {
		return fmt.Sprintf(" under alias %s", operation.ParentAlias)
	}
	return ""
}

func permissionOverwriteSuffix(operation channelPlanOperation) string {
	if len(operation.PermissionOverwrites) == 0 {
		return ""
	}
	return " with permission overwrites " + formatPlanPermissionOverwrites(operation.PermissionOverwrites)
}

func formatPlanPermissionOverwrites(overwrites []channelPlanPermissionOverwrite) string {
	parts := make([]string, 0, len(overwrites))
	for _, overwrite := range overwrites {
		target := overwrite.RoleName
		if target == "" {
			target = overwrite.RoleID
		}
		parts = append(parts, fmt.Sprintf("@%s allow=%s deny=%s", target, normalizePermissionBits(overwrite.Allow), normalizePermissionBits(overwrite.Deny)))
	}
	return "[" + strings.Join(parts, "; ") + "]"
}

func formatResolvedPermissionOverwrites(overwrites []PermissionOverwrite) string {
	parts := make([]string, 0, len(overwrites))
	for _, overwrite := range overwrites {
		parts = append(parts, fmt.Sprintf("@%s allow=%s deny=%s", overwrite.ID, normalizePermissionBits(overwrite.Allow), normalizePermissionBits(overwrite.Deny)))
	}
	return "[" + strings.Join(parts, "; ") + "]"
}
