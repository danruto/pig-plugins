package store

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

type scanRule struct {
	id      string
	pattern *regexp.Regexp
}

var contentRules = []scanRule{
	{"prompt_injection", regexp.MustCompile(`(?i)ignore\s+(previous|all|above|prior)\s+instructions`)},
	{"role_hijack", regexp.MustCompile(`(?i)you\s+are\s+now\s+`)},
	{"deception_hide", regexp.MustCompile(`(?i)do\s+not\s+tell\s+the\s+user`)},
	{"sys_prompt_override", regexp.MustCompile(`(?i)system\s+prompt\s+override`)},
	{"disregard_rules", regexp.MustCompile(`(?i)disregard\s+(your|all|any)\s+(instructions|rules|guidelines)`)},
	{"bypass_restrictions", regexp.MustCompile(`(?i)act\s+as\s+(if|though)\s+you\s+(have\s+no|don'?t\s+have)\s+(restrictions|limits|rules)`)},
	{"exfil_curl", regexp.MustCompile(`(?i)curl\s+[^\n]*\$\{?\w*(KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL|API)`)},
	{"exfil_wget", regexp.MustCompile(`(?i)wget\s+[^\n]*\$\{?\w*(KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL|API)`)},
	{"read_secrets", regexp.MustCompile(`(?i)cat\s+[^\n]*(\.env|credentials|\.netrc|\.pgpass|\.npmrc|\.pypirc)`)},
	{"ssh_backdoor", regexp.MustCompile(`(?i)authorized_keys`)},
	{"ssh_access", regexp.MustCompile(`(?i)\$HOME/\.ssh|~/\.ssh`)},
	{"anthropic_api_key", regexp.MustCompile(`\bsk-ant-api\S{10,}\b`)},
	{"openrouter_api_key", regexp.MustCompile(`\bsk-or-v1-\S{10,}\b`)},
	{"openai_api_key", regexp.MustCompile(`\bsk-\S{20,}\b`)},
	{"aws_access_key", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"github_personal_token", regexp.MustCompile(`\bghp_\S{10,}\b`)},
	{"github_user_token", regexp.MustCompile(`\bghu_\S{10,}\b`)},
	{"slack_bot_token", regexp.MustCompile(`\bxoxb-\S{10,}\b`)},
	{"slack_app_token", regexp.MustCompile(`\bxapp-\S{10,}\b`)},
	{"notion_token", regexp.MustCompile(`\bntn_\S{10,}\b`)},
	{"bearer_auth_token", regexp.MustCompile(`\bBearer\s+\S{20,}\b`)},
	{"private_key_block", regexp.MustCompile(`-----BEGIN\s+(?:RSA\s+)?PRIVATE\sKEY-----`)},
	{"env_anthropic_key", regexp.MustCompile(`\bANTHROPIC_API_KEY\b`)},
	{"env_openai_key", regexp.MustCompile(`\bOPENAI_API_KEY\b`)},
	{"env_openrouter_key", regexp.MustCompile(`\bOPENROUTER_API_KEY\b`)},
	{"env_github_token", regexp.MustCompile(`\bGITHUB_TOKEN\b`)},
	{"env_aws_secret", regexp.MustCompile(`\bAWS_SECRET_ACCESS_KEY\b`)},
	{"env_database_url", regexp.MustCompile(`\bDATABASE_URL\b`)},
	{"password_assignment", regexp.MustCompile(`(?i)\bpassword\s*[=:]\s*\S{6,}\b`)},
	{"secret_assignment", regexp.MustCompile(`(?i)\bsecret\s*[=:]\s*\S{6,}\b`)},
	{"token_assignment", regexp.MustCompile(`(?i)\btoken\s*[=:]\s*\S{10,}\b`)},
}

func scanContent(content string) error {
	for _, char := range content {
		if strings.ContainsRune("\u200b\u200c\u200d\u2060\ufeff\u202a\u202b\u202c\u202d\u202e", char) {
			return fmt.Errorf("blocked invisible unicode U+%04X", char)
		}
	}
	normalized := strings.Map(func(r rune) rune {
		if r != '\n' && unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, content)
	normalized = expandSupplementaryCharacters(normalized)
	for _, rule := range contentRules {
		if rule.pattern.MatchString(normalized) {
			return fmt.Errorf("blocked unsafe content: %s", rule.id)
		}
	}
	if strings.Contains(content, entryDelimiter) || strings.Contains(content, "<!--") {
		return fmt.Errorf("blocked reserved memory delimiter or metadata")
	}
	return nil
}

func expandSupplementaryCharacters(text string) string {
	var expanded strings.Builder
	for _, r := range text {
		if r > 0xffff {
			expanded.WriteString("\ufffd\ufffd")
		} else {
			expanded.WriteRune(r)
		}
	}
	return expanded.String()
}
