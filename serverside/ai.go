package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

func (h *BackendLogicHook) requestGeminiSuggestion(summary SensorBatchSummary, weather WeatherSummary, gardenCfg GardenConfigState) (string, error) {
	if h.cfg.GeminiAPIKey == "" || strings.HasPrefix(h.cfg.GeminiAPIKey, "your_") {
		return "", fmt.Errorf("GEMINI_API_KEY is missing")
	}

	model := strings.TrimSpace(h.cfg.GeminiModel)
	if model == "" || strings.HasPrefix(model, "your_") {
		model = "gemini-2.5-flash"
	}

	maxRetries := h.cfg.GeminiMaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}

	prompt := fmt.Sprintf(
		"Ban la tro ly nong nghiep thong minh cho cay %s. Nguong hien tai: bat bom khi do am dat <= %.1f%%, tat bom khi >= %.1f%%. Day la %d mau cam bien gan nhat: nhiet do TB=%.1fC (min %.1f, max %.1f), do am khong khi TB=%.1f%% (min %.1f, max %.1f), do am dat TB=%.1f%% (min %.1f, max %.1f), mau moi nhat do am dat=%.1f%%. Thoi tiet hien tai: %s, nhiet do %.1fC, do am %d%%. Hay dua ra 1-2 cau bang tieng Viet tu nhien: co nen tuoi khong, neu can thi noi ro co nen tang/giam nguong do am cho loai cay nay khong.",
		gardenCfg.PlantType,
		gardenCfg.SoilPumpOnBelow,
		gardenCfg.SoilPumpOffAbove,
		summary.Count,
		summary.Avg.Temperature,
		summary.Min.Temperature,
		summary.Max.Temperature,
		summary.Avg.Humidity,
		summary.Min.Humidity,
		summary.Max.Humidity,
		summary.Avg.SoilMoisture,
		summary.Min.SoilMoisture,
		summary.Max.SoilMoisture,
		summary.Latest.SoilMoisture,
		weather.Description,
		weather.Temperature,
		weather.Humidity,
	)

	requestBody := map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]string{{"text": prompt}},
			},
		},
	}

	encoded, err := json.Marshal(requestBody)
	if err != nil {
		return "", err
	}

	models := uniqueNonEmpty([]string{model, "gemini-2.5-flash", "gemini-2.5-flash-lite"})
	var lastErr error

	for _, modelName := range models {
		for attempt := 1; attempt <= maxRetries; attempt++ {
			text, statusCode, err := h.callGeminiGenerate(modelName, encoded)
			if err == nil {
				if modelName != model {
					log.Printf("[AI Agent] switched model to %s", modelName)
				}
				return text, nil
			}

			lastErr = err
			if !shouldRetryGemini(statusCode, err) || attempt == maxRetries {
				break
			}

			delay := geminiRetryDelay(attempt)
			log.Printf("[AI Agent] gemini retry model=%s attempt=%d/%d after %s (%v)", modelName, attempt, maxRetries, delay, err)
			time.Sleep(delay)
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("gemini request failed")
	}
	return "", lastErr
}

func (h *BackendLogicHook) callGeminiGenerate(model string, encoded []byte) (string, int, error) {
	endpoint := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", model)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", h.cfg.GeminiAPIKey)

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return "", resp.StatusCode, err
	}

	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, fmt.Errorf("gemini status %d: %s", resp.StatusCode, string(body))
	}

	var geminiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}

	if err := json.Unmarshal(body, &geminiResp); err != nil {
		return "", http.StatusOK, err
	}

	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return "", http.StatusOK, fmt.Errorf("gemini returned no content")
	}

	text := strings.TrimSpace(geminiResp.Candidates[0].Content.Parts[0].Text)
	if text == "" {
		return "", http.StatusOK, fmt.Errorf("gemini empty response")
	}

	return text, http.StatusOK, nil
}

func shouldRetryGemini(statusCode int, err error) bool {
	if err == nil {
		return false
	}

	switch statusCode {
	case 0:
		return true
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func geminiRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := time.Second
	delay := base * time.Duration(1<<(attempt-1))
	if delay > 8*time.Second {
		return 8 * time.Second
	}
	return delay
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))

	for _, v := range values {
		normalized := strings.TrimSpace(v)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}

	return out
}

func fallbackSuggestion(summary SensorBatchSummary, weather WeatherSummary, gardenCfg GardenConfigState) string {
	if summary.Avg.SoilMoisture <= gardenCfg.SoilPumpOnBelow {
		if strings.Contains(strings.ToLower(weather.Description), "mua") {
			return fmt.Sprintf("Dat cua %s dang kho nhung troi co mua, tam hoan tuoi 15-30 phut de tiet kiem nuoc.", gardenCfg.PlantType)
		}
		return fmt.Sprintf("Do am dat cua %s thap, nen bat bom tuoi 3-5 phut.", gardenCfg.PlantType)
	}
	if summary.Avg.SoilMoisture >= gardenCfg.SoilPumpOffAbove {
		return fmt.Sprintf("Do am dat cua %s da du, nen tat bom va theo doi them.", gardenCfg.PlantType)
	}
	return fmt.Sprintf("Do am dat cua %s dang nam giua nguong tuoi, chua can doi trang thai bom.", gardenCfg.PlantType)
}
