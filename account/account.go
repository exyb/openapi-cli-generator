package account

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/danielgtaylor/openapi-toolkit/cli"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
)

const (
	aesKey = "change-key-here-"
	aesIV  = "change-iv-here--"
)

type config struct {
	typeName    string
	extra       []string
	getServerURL func() string
}

type Handler struct {
	TypeName     string
	Keys         []string
	GetServerURL func() string
}

func (h *Handler) ProfileKeys() []string {
	return h.Keys
}

func (h *Handler) OnRequest(log *zerolog.Logger, request *http.Request) error {
	if request.Header.Get("Authorization") == "" {
		profile := cli.GetProfile()

		credentials := profile["credentials"]
		loginURL := profile["url"]

		if credentials == "" || loginURL == "" {
			return fmt.Errorf("missing credentials or login URL for profile %s", viper.GetString("profile"))
		}

		tokenKey := "profiles." + viper.GetString("profile") + ".token"
		expiresKey := "profiles." + viper.GetString("profile") + ".expires"
		typeKey := "profiles." + viper.GetString("profile") + ".type"

		token := cli.Cache.GetString(tokenKey)
		expiry := cli.Cache.GetTime(expiresKey)

		needRefresh := token == "" || (!expiry.IsZero() && time.Now().After(expiry.Add(-5*time.Minute)))

		if needRefresh {
			username, password, err := decryptCredentials(credentials)
			if err != nil {
				return fmt.Errorf("failed to decrypt credentials: %w", err)
			}

			newToken, newExpiry, err := loginAndFetchToken(loginURL, username, password, h.GetServerURL)
			if err != nil {
				return fmt.Errorf("failed to login: %w", err)
			}

			cli.Cache.Set(tokenKey, newToken)
			cli.Cache.Set(expiresKey, newExpiry)
			cli.Cache.Set(typeKey, h.TypeName)

			if err := cli.Cache.WriteConfig(); err != nil {
				return err
			}

			token = newToken
			log.Debug().Msg("Token refreshed from login")
		} else {
			log.Debug().Msg("Using cached token")
		}

		request.Header.Set("Authorization", "Bearer "+token)
	}

	return nil
}

func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty data")
	}
	padding := int(data[len(data)-1])
	if padding > len(data) || padding > aes.BlockSize {
		return nil, fmt.Errorf("invalid padding")
	}
	return data[:len(data)-padding], nil
}

func decryptCredentials(encrypted string) (username, password string, err error) {
	ciphertext, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return "", "", fmt.Errorf("base64 decode failed: %w", err)
	}

	block, err := aes.NewCipher([]byte(aesKey))
	if err != nil {
		return "", "", fmt.Errorf("create cipher failed: %w", err)
	}

	if len(ciphertext) < aes.BlockSize {
		return "", "", fmt.Errorf("ciphertext too short")
	}

	if len(ciphertext)%aes.BlockSize != 0 {
		return "", "", fmt.Errorf("ciphertext not multiple of block size")
	}

	mode := cipher.NewCBCDecrypter(block, []byte(aesIV))
	decrypted := make([]byte, len(ciphertext))
	mode.CryptBlocks(decrypted, ciphertext)

	decrypted, err = pkcs7Unpad(decrypted)
	if err != nil {
		return "", "", fmt.Errorf("unpad failed: %w", err)
	}

	parts := strings.SplitN(string(decrypted), ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid credentials format")
	}

	return parts[0], parts[1], nil
}

type loginResponse struct {
	Data struct {
		Token     string `json:"token"`
		ExpiresIn int    `json:"expiresIn"`
	} `json:"data"`
	Success bool   `json:"success"`
	Message string `json:"message"`
}

type jwtPayload struct {
	Exp int64  `json:"exp"`
	Iat int64  `json:"iat"`
	Sub string `json:"sub"`
}

func parseJWTExpiry(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("invalid JWT format")
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("decode payload failed: %w", err)
	}

	var payload jwtPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return time.Time{}, fmt.Errorf("parse payload failed: %w", err)
	}

	if payload.Exp == 0 {
		return time.Time{}, fmt.Errorf("no exp in JWT")
	}

	return time.Unix(payload.Exp, 0), nil
}

func loginAndFetchToken(loginURL, username, password string, getServerURL func() string) (string, time.Time, error) {
	serverURL := viper.GetString("server")
	if serverURL == "" && getServerURL != nil {
		serverURL = getServerURL()
	}
	if serverURL == "" {
		return "", time.Time{}, fmt.Errorf("server URL is not configured, use --server flag or config file")
	}

	fullURL := serverURL + loginURL

	body := map[string]string{
		"username": username,
		"password": password,
	}
	jsonBody, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", fullURL, bytes.NewReader(jsonBody))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()

	respBody, _ := ioutil.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return "", time.Time{}, fmt.Errorf("login failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var loginResp loginResponse
	if err := json.Unmarshal(respBody, &loginResp); err != nil {
		return "", time.Time{}, fmt.Errorf("parse response failed: %w", err)
	}

	if !loginResp.Success {
		return "", time.Time{}, fmt.Errorf("login failed: %s", loginResp.Message)
	}

	token := loginResp.Data.Token

	var expiry time.Time
	if loginResp.Data.ExpiresIn > 0 {
		expiry = time.Now().Add(time.Duration(loginResp.Data.ExpiresIn) * time.Second)
	} else {
		expiry, err = parseJWTExpiry(token)
		if err != nil {
			expiry = time.Now().Add(30 * time.Minute)
		}
	}

	return token, expiry, nil
}

func ForceRefresh() {
	tokenKey := "profiles." + viper.GetString("profile") + ".token"
	expiresKey := "profiles." + viper.GetString("profile") + ".expires"
	cli.Cache.Set(tokenKey, "")
	cli.Cache.Set(expiresKey, time.Time{})
}

func Extra(names ...string) func(*config) error {
	return func(c *config) error {
		c.extra = names
		return nil
	}
}

func Type(name string) func(*config) error {
	return func(c *config) error {
		c.typeName = name
		return nil
	}
}

func ServerURL(f func() string) func(*config) error {
	return func(c *config) error {
		c.getServerURL = f
		return nil
	}
}

func Init(envPrefix string, options ...func(*config) error) {
	var c config
	c.typeName = "account"

	for _, option := range options {
		if err := option(&c); err != nil {
			panic(err)
		}
	}

	handler := &Handler{
		TypeName:     c.typeName,
		Keys:         append([]string{"credentials", "url"}, c.extra...),
		GetServerURL: c.getServerURL,
	}

	cli.UseAuth(c.typeName, handler)

	if envPrefix == "" {
		envPrefix = "APP"
	}
	envKey := envPrefix + "_ACCOUNT_CREDENTIALS"
	envValue := os.Getenv(envKey)

	if envValue != "" {
		parts := strings.SplitN(envValue, ":", 2)
		if len(parts) == 2 {
			credentials := parts[0]
			loginURL := parts[1]

			cli.Creds.Set("profiles.default.credentials", credentials)
			cli.Creds.Set("profiles.default.url", loginURL)
			cli.Creds.Set("profiles.default.type", c.typeName)

			filename := viper.GetString("config-directory")
			if filename != "" {
				if err := cli.Creds.WriteConfigAs(filename + "/credentials.json"); err != nil {
					panic(err)
				}
			}
		}
	}
}
