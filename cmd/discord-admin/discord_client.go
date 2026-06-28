package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const defaultDiscordAPIBaseURL = "https://discord.com/api/v10"

type DiscordClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
	userAgent  string
}

type DiscordClientOption func(*DiscordClient)

func NewDiscordClient(botToken string, options ...DiscordClientOption) *DiscordClient {
	client := &DiscordClient{
		baseURL:    defaultDiscordAPIBaseURL,
		token:      botToken,
		httpClient: http.DefaultClient,
		userAgent:  "discord-admin/0.1",
	}
	for _, option := range options {
		option(client)
	}
	client.baseURL = strings.TrimRight(client.baseURL, "/")
	if client.httpClient == nil {
		client.httpClient = http.DefaultClient
	}
	return client
}

func WithBaseURL(baseURL string) DiscordClientOption {
	return func(client *DiscordClient) {
		client.baseURL = baseURL
	}
}

func WithHTTPClient(httpClient *http.Client) DiscordClientOption {
	return func(client *DiscordClient) {
		client.httpClient = httpClient
	}
}

type DiscordUser struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	GlobalName string `json:"global_name,omitempty"`
	Bot        bool   `json:"bot,omitempty"`
}

type DiscordRole struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ChannelType int

const (
	ChannelTypeGuildText     ChannelType = 0
	ChannelTypeGuildVoice    ChannelType = 2
	ChannelTypeGuildCategory ChannelType = 4
)

type PermissionOverwriteType int

const (
	PermissionOverwriteTypeRole   PermissionOverwriteType = 0
	PermissionOverwriteTypeMember PermissionOverwriteType = 1
)

type PermissionOverwrite struct {
	ID    string                  `json:"id"`
	Type  PermissionOverwriteType `json:"type"`
	Allow string                  `json:"allow"`
	Deny  string                  `json:"deny"`
}

type DiscordChannel struct {
	ID                   string                `json:"id"`
	GuildID              string                `json:"guild_id,omitempty"`
	Name                 string                `json:"name"`
	Type                 ChannelType           `json:"type"`
	ParentID             *string               `json:"parent_id,omitempty"`
	Position             int                   `json:"position,omitempty"`
	PermissionOverwrites []PermissionOverwrite `json:"permission_overwrites,omitempty"`
}

type CreateGuildChannelRequest struct {
	Name                 string                `json:"name"`
	Type                 ChannelType           `json:"type"`
	ParentID             *string               `json:"parent_id,omitempty"`
	Position             *int                  `json:"position,omitempty"`
	PermissionOverwrites []PermissionOverwrite `json:"permission_overwrites,omitempty"`
}

type UpdateChannelRequest struct {
	Name            *string `json:"name,omitempty"`
	ParentID        *string `json:"parent_id,omitempty"`
	Position        *int    `json:"position,omitempty"`
	LockPermissions *bool   `json:"lock_permissions,omitempty"`
}

func (client *DiscordClient) GetCurrentUser(ctx context.Context) (DiscordUser, error) {
	var user DiscordUser
	err := client.doJSON(ctx, http.MethodGet, "/users/@me", nil, &user)
	return user, err
}

func (client *DiscordClient) GetChannel(ctx context.Context, channelID string) (DiscordChannel, error) {
	var channel DiscordChannel
	err := client.doJSON(ctx, http.MethodGet, "/channels/"+url.PathEscape(channelID), nil, &channel)
	return channel, err
}

func (client *DiscordClient) GetGuildChannels(ctx context.Context, guildID string) ([]DiscordChannel, error) {
	var channels []DiscordChannel
	err := client.doJSON(ctx, http.MethodGet, "/guilds/"+url.PathEscape(guildID)+"/channels", nil, &channels)
	return channels, err
}

func (client *DiscordClient) GetGuildRoles(ctx context.Context, guildID string) ([]DiscordRole, error) {
	var roles []DiscordRole
	err := client.doJSON(ctx, http.MethodGet, "/guilds/"+url.PathEscape(guildID)+"/roles", nil, &roles)
	return roles, err
}

func (client *DiscordClient) CreateGuildChannel(ctx context.Context, guildID string, request CreateGuildChannelRequest) (DiscordChannel, error) {
	var channel DiscordChannel
	err := client.doJSON(ctx, http.MethodPost, "/guilds/"+url.PathEscape(guildID)+"/channels", request, &channel)
	return channel, err
}

func (client *DiscordClient) UpdateChannel(ctx context.Context, channelID string, request UpdateChannelRequest) (DiscordChannel, error) {
	var channel DiscordChannel
	err := client.doJSON(ctx, http.MethodPatch, "/channels/"+url.PathEscape(channelID), request, &channel)
	return channel, err
}

func (client *DiscordClient) doJSON(ctx context.Context, method string, path string, body any, response any) error {
	var requestBody io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode %s %s request: %w", method, path, err)
		}
		requestBody = bytes.NewReader(payload)
	}

	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, requestBody)
	if err != nil {
		return fmt.Errorf("build %s %s request: %w", method, path, err)
	}
	request.Header.Set("Authorization", "Bot "+client.token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", client.userAgent)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("discord API %s %s request failed: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return client.responseError(resp, method, path)
	}
	if response == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(response); err != nil {
		return fmt.Errorf("decode %s %s response: %w", method, path, err)
	}
	return nil
}

type discordAPIError struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

func (client *DiscordClient) responseError(resp *http.Response, method string, path string) error {
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	detail := resp.Status
	if readErr == nil && len(body) > 0 {
		var apiError discordAPIError
		if err := json.Unmarshal(body, &apiError); err == nil && apiError.Message != "" {
			detail = apiError.Message
			if apiError.Code != 0 {
				detail = fmt.Sprintf("%s (code %d)", detail, apiError.Code)
			}
		} else if text := strings.TrimSpace(string(body)); text != "" {
			detail = text
		}
	}
	detail = redactSecret(detail, client.token)

	return fmt.Errorf("discord API %s %s failed: %s: %s", method, path, resp.Status, detail)
}

func redactSecret(text string, secret string) string {
	if secret == "" {
		return text
	}
	return strings.ReplaceAll(text, secret, "[redacted]")
}
