package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (h *BackendLogicHook) fetchCurrentWeather() (WeatherSummary, error) {
	provider := strings.ToLower(strings.TrimSpace(h.cfg.WeatherProvider))
	switch provider {
	case "", "openweather", "openweathermap":
		return h.fetchCurrentWeatherOpenWeather()
	case "weatherapi", "weather.com", "weatherdotcom":
		return h.fetchCurrentWeatherWeatherAPI()
	default:
		return WeatherSummary{}, fmt.Errorf("unsupported WEATHER_PROVIDER: %s", h.cfg.WeatherProvider)
	}
}

func (h *BackendLogicHook) fetchCurrentWeatherOpenWeather() (WeatherSummary, error) {
	if h.cfg.OpenWeatherAPIKey == "" {
		return WeatherSummary{}, fmt.Errorf("OPENWEATHERMAP_API_KEY is empty")
	}

	url := fmt.Sprintf(
		"https://api.openweathermap.org/data/2.5/weather?lat=%.6f&lon=%.6f&appid=%s&units=metric&lang=vi",
		h.cfg.WeatherLat,
		h.cfg.WeatherLon,
		h.cfg.OpenWeatherAPIKey,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return WeatherSummary{}, err
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return WeatherSummary{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return WeatherSummary{}, fmt.Errorf("openweather status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Weather []struct {
			Description string `json:"description"`
		} `json:"weather"`
		Main struct {
			Temp     float64 `json:"temp"`
			Humidity int     `json:"humidity"`
		} `json:"main"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return WeatherSummary{}, err
	}

	desc := "unknown"
	if len(result.Weather) > 0 && result.Weather[0].Description != "" {
		desc = result.Weather[0].Description
	}

	return WeatherSummary{
		Description: desc,
		Temperature: result.Main.Temp,
		Humidity:    result.Main.Humidity,
	}, nil
}

func (h *BackendLogicHook) fetchCurrentWeatherWeatherAPI() (WeatherSummary, error) {
	if h.cfg.WeatherAPIKey == "" {
		return WeatherSummary{}, fmt.Errorf("WEATHERAPI_KEY is empty")
	}

	q := fmt.Sprintf("%.6f,%.6f", h.cfg.WeatherLat, h.cfg.WeatherLon)
	values := url.Values{}
	values.Set("key", h.cfg.WeatherAPIKey)
	values.Set("q", q)
	values.Set("lang", "vi")

	endpoint := "https://api.weatherapi.com/v1/current.json?" + values.Encode()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return WeatherSummary{}, err
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return WeatherSummary{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return WeatherSummary{}, fmt.Errorf("weatherapi status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Current struct {
			TempC     float64 `json:"temp_c"`
			Humidity  int     `json:"humidity"`
			Condition struct {
				Text string `json:"text"`
			} `json:"condition"`
		} `json:"current"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return WeatherSummary{}, err
	}

	desc := strings.TrimSpace(result.Current.Condition.Text)
	if desc == "" {
		desc = "unknown"
	}

	return WeatherSummary{
		Description: desc,
		Temperature: result.Current.TempC,
		Humidity:    result.Current.Humidity,
	}, nil
}
