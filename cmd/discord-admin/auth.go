package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
)

const discordBotTokenEnv = "DISCORD_BOT_TOKEN"

type AuthOptions struct {
	EnvFile string
}

type Auth struct {
	BotToken string
}

func (a Auth) String() string {
	return "Auth{BotToken:[redacted]}"
}

func (a Auth) GoString() string {
	return a.String()
}

func loadAuth(options AuthOptions) (Auth, error) {
	if token := strings.TrimSpace(os.Getenv(discordBotTokenEnv)); token != "" {
		return Auth{BotToken: token}, nil
	}

	if options.EnvFile != "" {
		values, err := readEnvFile(options.EnvFile)
		if err != nil {
			return Auth{}, err
		}
		if token := strings.TrimSpace(values[discordBotTokenEnv]); token != "" {
			return Auth{BotToken: token}, nil
		}
	}

	return Auth{}, fmt.Errorf("%s is not set", discordBotTokenEnv)
}

func readEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read env file %s: %w", path, err)
	}
	defer file.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}

		key, rawValue, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("invalid env file %s line %d: expected KEY=VALUE", path, lineNumber)
		}

		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, " \t") {
			return nil, fmt.Errorf("invalid env file %s line %d: invalid key", path, lineNumber)
		}

		value, err := parseEnvValue(rawValue)
		if err != nil {
			return nil, fmt.Errorf("invalid env file %s line %d: %w", path, lineNumber, err)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read env file %s: %w", path, err)
	}

	return values, nil
}

func parseEnvValue(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}

	quote := value[0]
	if quote != '\'' && quote != '"' {
		return value, nil
	}
	if len(value) < 2 || value[len(value)-1] != quote {
		return "", errors.New("unterminated quoted value")
	}

	return value[1 : len(value)-1], nil
}
