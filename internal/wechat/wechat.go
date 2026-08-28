package wechat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type Account struct {
	BaseURL       string    `json:"base_url"`
	BotID         string    `json:"bot_id"`
	BoundUserID   string    `json:"bound_user_id"`
	Workspace     string    `json:"workspace"`
	TokenRedacted string    `json:"token_redacted"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func Command(home string) *cobra.Command {
	root := &cobra.Command{Use: "wechat", Short: "manage optional WeChat iLink channel"}
	root.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "show WeChat binding status",
		RunE: func(cmd *cobra.Command, args []string) error {
			acct, err := load(home)
			if err != nil {
				fmt.Println("WeChat channel: not configured")
				fmt.Println("Run `zcoder wechat setup --base-url ... --bot-id ... --token ...` to bind.")
				return nil
			}
			fmt.Printf("WeChat channel: configured\nbase_url: %s\nbot_id: %s\nbound_user: %s\nworkspace: %s\nupdated: %s\n",
				acct.BaseURL, acct.BotID, redact(acct.BoundUserID), acct.Workspace, acct.UpdatedAt.Format(time.RFC3339))
			return nil
		},
	})
	var setup setupOptions
	setupCmd := &cobra.Command{
		Use:   "setup",
		Short: "save iLink account settings; QR login is implemented by the provider-specific iLink endpoint",
		RunE: func(cmd *cobra.Command, args []string) error {
			if setup.BaseURL == "" || setup.BotID == "" || setup.Token == "" || setup.BoundUserID == "" {
				return fmt.Errorf("--base-url, --bot-id, --token and --bound-user-id are required")
			}
			cwd, _ := os.Getwd()
			acct := Account{
				BaseURL:       strings.TrimRight(setup.BaseURL, "/"),
				BotID:         setup.BotID,
				BoundUserID:   setup.BoundUserID,
				Workspace:     cwd,
				TokenRedacted: redact(setup.Token),
				UpdatedAt:     time.Now(),
			}
			if err := save(home, acct, setup.Token); err != nil {
				return err
			}
			fmt.Println("WeChat account saved. Remote dangerous tools use non-interactive deny-by-default policy.")
			return nil
		},
	}
	setupCmd.Flags().StringVar(&setup.BaseURL, "base-url", "", "iLink base URL")
	setupCmd.Flags().StringVar(&setup.BotID, "bot-id", "", "iLink bot id")
	setupCmd.Flags().StringVar(&setup.Token, "token", "", "iLink token")
	setupCmd.Flags().StringVar(&setup.BoundUserID, "bound-user-id", "", "bound WeChat user id")
	root.AddCommand(setupCmd)
	root.AddCommand(&cobra.Command{
		Use:   "start",
		Short: "start foreground WeChat loop",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := load(home)
			if err != nil {
				return fmt.Errorf("WeChat channel not configured; run setup first")
			}
			fmt.Println("WeChat foreground loop is not started in this baseline. Account, formatter and policy are ready; media/long-poll daemon remains a follow-up.")
			return nil
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "daemon",
		Short: "manage WeChat daemon",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("WeChat daemon supervisor is not installed yet. Use `wechat start` for foreground validation after media/long-poll is completed.")
		},
	})
	return root
}

type setupOptions struct {
	BaseURL     string
	BotID       string
	Token       string
	BoundUserID string
}

func load(home string) (Account, error) {
	var acct Account
	b, err := os.ReadFile(filepath.Join(home, ".zcoder", "wechat", "account.json"))
	if err != nil {
		return acct, err
	}
	return acct, json.Unmarshal(b, &acct)
}

func save(home string, acct Account, token string) error {
	dir := filepath.Join(home, ".zcoder", "wechat")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(acct, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "account.json"), b, 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "token"), []byte(token), 0o600)
}

func FormatForWeChat(markdown string) string {
	lines := strings.Split(markdown, "\n")
	var out []string
	inCode := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "```") {
			inCode = !inCode
			out = append(out, trim)
			continue
		}
		if inCode {
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(trim, "######") || strings.HasPrefix(trim, "#####") {
			continue
		}
		if strings.HasPrefix(trim, "#") {
			title := strings.TrimSpace(strings.TrimLeft(trim, "#"))
			out = append(out, "**"+title+"**")
			continue
		}
		if strings.Contains(trim, "|") && strings.Count(trim, "|") >= 2 {
			out = append(out, strings.ReplaceAll(trim, "|", " / "))
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func redact(s string) string {
	if len(s) <= 8 {
		return "***"
	}
	return s[:4] + "..." + s[len(s)-4:]
}
