package agentcore

import (
	"encoding/json"
	"sort"
	"strings"
)

// CatalogModel is one model entry of the provider catalog (models.dev
// shape, §10.1).
type CatalogModel struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Reasoning  bool   `json:"reasoning"`
	ToolCall   bool   `json:"tool_call"`
	Attachment bool   `json:"attachment"`
	Cost       struct {
		Input      float64 `json:"input"`
		Output     float64 `json:"output"`
		CacheRead  float64 `json:"cache_read"`
		CacheWrite float64 `json:"cache_write"`
	} `json:"cost"`
	Limit struct {
		Context int `json:"context"`
		Output  int `json:"output"`
	} `json:"limit"`
	Modalities struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Status string `json:"status"`
}

// CatalogProvider is one provider of the catalog (models.dev shape).
type CatalogProvider struct {
	ID     string                  `json:"id"`
	Name   string                  `json:"name"`
	Env    []string                `json:"env"`
	NPM    string                  `json:"npm"`
	API    string                  `json:"api"`
	Models map[string]CatalogModel `json:"models"`
}

// catalogType maps a catalog provider to a Maestro provider type.
func catalogType(p CatalogProvider) string {
	if strings.Contains(p.NPM, "anthropic") || strings.Contains(p.API, "anthropic.com") {
		return "anthropic"
	}
	return "openai-compat"
}

// toModel converts a catalog model to a Model.
func toModel(id string, m CatalogModel) Model {
	model := Model{
		ID:               id,
		Name:             m.Name,
		ContextWindow:    m.Limit.Context,
		DefaultMaxTokens: m.Limit.Output,
		CanReason:        m.Reasoning,
		SupportsImages:   containsStr(m.Modalities.Input, "image"),
		PriceInput:       m.Cost.Input,
		PriceOutput:      m.Cost.Output,
		PriceCacheCreate: m.Cost.CacheWrite,
		PriceCacheHit:    m.Cost.CacheRead,
	}
	if model.ContextWindow <= 0 {
		model.ContextWindow = 128_000
	}
	if model.DefaultMaxTokens <= 0 {
		model.DefaultMaxTokens = 4096
	}
	if m.Reasoning {
		model.ReasoningEffort = "medium"
	}
	return model
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// catalogModels converts a catalog provider's models into the static model
// list for a registry provider.
func catalogModels(p CatalogProvider) []Model {
	ids := make([]string, 0, len(p.Models))
	for id := range p.Models {
		if p.Models[id].Status == "deprecated" {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Model, 0, len(ids))
	for _, id := range ids {
		out = append(out, toModel(id, p.Models[id]))
	}
	return out
}

// ParseCatalog decodes a models.dev api.json payload into providers.
func ParseCatalog(data []byte) (map[string]CatalogProvider, error) {
	var out map[string]CatalogProvider
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// coreCatalog is the embedded fallback snapshot (§10.1): the essential
// providers with real metadata, used when the remote catalog is
// unreachable or disabled. The remote models.dev catalog covers the other
// 160+ providers.
var coreCatalogJSON = `{
  "openai": {
    "id": "openai", "name": "OpenAI", "env": ["OPENAI_API_KEY"],
    "npm": "@ai-sdk/openai", "api": "https://api.openai.com/v1",
    "models": {
      "gpt-5": {"id":"gpt-5","name":"GPT-5","reasoning":true,"tool_call":true,"attachment":true,
        "cost":{"input":1.25,"output":10,"cache_read":0.125,"cache_write":1.25},
        "limit":{"context":400000,"output":100000},"modalities":{"input":["text","image"],"output":["text"]},"status":"active"},
      "gpt-5-mini": {"id":"gpt-5-mini","name":"GPT-5 mini","reasoning":true,"tool_call":true,"attachment":true,
        "cost":{"input":0.25,"output":2,"cache_read":0.025,"cache_write":0.25},
        "limit":{"context":400000,"output":65536},"modalities":{"input":["text","image"],"output":["text"]},"status":"active"},
      "gpt-4.1": {"id":"gpt-4.1","name":"GPT-4.1","tool_call":true,"attachment":true,
        "cost":{"input":2,"output":8,"cache_read":0.2,"cache_write":2},
        "limit":{"context":1047576,"output":32768},"modalities":{"input":["text","image"],"output":["text"]},"status":"active"},
      "gpt-4.1-mini": {"id":"gpt-4.1-mini","name":"GPT-4.1 mini","tool_call":true,"attachment":true,
        "cost":{"input":0.4,"output":1.6,"cache_read":0.04,"cache_write":0.4},
        "limit":{"context":1047576,"output":32768},"modalities":{"input":["text","image"],"output":["text"]},"status":"active"},
      "o3": {"id":"o3","name":"o3","reasoning":true,"tool_call":true,"attachment":true,
        "cost":{"input":2,"output":8,"cache_read":0.5,"cache_write":2},
        "limit":{"context":200000,"output":100000},"modalities":{"input":["text","image"],"output":["text"]},"status":"active"},
      "gpt-4o": {"id":"gpt-4o","name":"GPT-4o","tool_call":true,"attachment":true,
        "cost":{"input":2.5,"output":10,"cache_read":1.25,"cache_write":2.5},
        "limit":{"context":128000,"output":16384},"modalities":{"input":["text","image"],"output":["text"]},"status":"active"},
      "gpt-4o-mini": {"id":"gpt-4o-mini","name":"GPT-4o mini","tool_call":true,"attachment":true,
        "cost":{"input":0.15,"output":0.6,"cache_read":0.075,"cache_write":0.15},
        "limit":{"context":128000,"output":16384},"modalities":{"input":["text","image"],"output":["text"]},"status":"active"}
    }
  },
  "anthropic": {
    "id": "anthropic", "name": "Anthropic", "env": ["ANTHROPIC_API_KEY"],
    "npm": "@ai-sdk/anthropic", "api": "https://api.anthropic.com",
    "models": {
      "claude-opus-4-1": {"id":"claude-opus-4-1","name":"Claude Opus 4.1","tool_call":true,"attachment":true,
        "cost":{"input":15,"output":75,"cache_read":1.5,"cache_write":18.75},
        "limit":{"context":200000,"output":64000},"modalities":{"input":["text","image"],"output":["text"]},"status":"active"},
      "claude-sonnet-4-5": {"id":"claude-sonnet-4-5","name":"Claude Sonnet 4.5","tool_call":true,"attachment":true,
        "cost":{"input":3,"output":15,"cache_read":0.3,"cache_write":3.75},
        "limit":{"context":200000,"output":64000},"modalities":{"input":["text","image"],"output":["text"]},"status":"active"},
      "claude-haiku-4-5": {"id":"claude-haiku-4-5","name":"Claude Haiku 4.5","tool_call":true,"attachment":true,
        "cost":{"input":1,"output":5,"cache_read":0.1,"cache_write":1.25},
        "limit":{"context":200000,"output":64000},"modalities":{"input":["text","image"],"output":["text"]},"status":"active"}
    }
  },
  "google": {
    "id": "google", "name": "Google", "env": ["GEMINI_API_KEY"],
    "npm": "@ai-sdk/google", "api": "https://generativelanguage.googleapis.com/v1beta/openai",
    "models": {
      "gemini-2.5-pro": {"id":"gemini-2.5-pro","name":"Gemini 2.5 Pro","reasoning":true,"tool_call":true,"attachment":true,
        "cost":{"input":1.25,"output":10,"cache_read":0.3125,"cache_write":1.25},
        "limit":{"context":1048576,"output":65536},"modalities":{"input":["text","image","pdf"],"output":["text"]},"status":"active"},
      "gemini-2.5-flash": {"id":"gemini-2.5-flash","name":"Gemini 2.5 Flash","reasoning":true,"tool_call":true,"attachment":true,
        "cost":{"input":0.3,"output":2.5,"cache_read":0.075,"cache_write":0.3},
        "limit":{"context":1048576,"output":65536},"modalities":{"input":["text","image","pdf"],"output":["text"]},"status":"active"}
    }
  },
  "deepseek": {
    "id": "deepseek", "name": "DeepSeek", "env": ["DEEPSEEK_API_KEY"],
    "npm": "@ai-sdk/openai-compatible", "api": "https://api.deepseek.com/v1",
    "models": {
      "deepseek-chat": {"id":"deepseek-chat","name":"DeepSeek Chat",
        "cost":{"input":0.27,"output":1.1,"cache_read":0.07,"cache_write":1.1},
        "limit":{"context":128000,"output":8192},"modalities":{"input":["text"],"output":["text"]},"status":"active"},
      "deepseek-reasoner": {"id":"deepseek-reasoner","name":"DeepSeek Reasoner","reasoning":true,
        "cost":{"input":0.55,"output":2.19,"cache_read":0.14,"cache_write":2.19},
        "limit":{"context":128000,"output":8192},"modalities":{"input":["text"],"output":["text"]},"status":"active"}
    }
  },
  "groq": {
    "id": "groq", "name": "Groq", "env": ["GROQ_API_KEY"],
    "npm": "@ai-sdk/groq", "api": "https://api.groq.com/openai/v1",
    "models": {
      "llama-3.3-70b-versatile": {"id":"llama-3.3-70b-versatile","name":"Llama 3.3 70B",
        "cost":{"input":0.59,"output":0.79},
        "limit":{"context":131072,"output":32768},"modalities":{"input":["text"],"output":["text"]},"status":"active"}
    }
  },
  "openrouter": {
    "id": "openrouter", "name": "OpenRouter", "env": ["OPENROUTER_API_KEY"],
    "npm": "@openrouter/ai-sdk-provider", "api": "https://openrouter.ai/api/v1",
    "models": {
      "auto": {"id":"auto","name":"OpenRouter Auto","tool_call":true,
        "cost":{"input":0,"output":0},
        "limit":{"context":128000,"output":32768},"modalities":{"input":["text"],"output":["text"]},"status":"active"}
    }
  },
  "xai": {
    "id": "xai", "name": "xAI", "env": ["XAI_API_KEY"],
    "npm": "@ai-sdk/xai", "api": "https://api.x.ai/v1",
    "models": {
      "grok-4": {"id":"grok-4","name":"Grok 4","reasoning":true,"tool_call":true,"attachment":true,
        "cost":{"input":0.6,"output":4,"cache_read":0.3,"cache_write":0.6},
        "limit":{"context":131072,"output":32768},"modalities":{"input":["text","image"],"output":["text"]},"status":"active"},
      "grok-3": {"id":"grok-3","name":"Grok 3","tool_call":true,"attachment":true,
        "cost":{"input":0.3,"output":0.6,"cache_read":0.15,"cache_write":0.3},
        "limit":{"context":131072,"output":32768},"modalities":{"input":["text","image"],"output":["text"]},"status":"active"}
    }
  },
  "mistral": {
    "id": "mistral", "name": "Mistral", "env": ["MISTRAL_API_KEY"],
    "npm": "@ai-sdk/mistral", "api": "https://api.mistral.ai/v1",
    "models": {
      "codestral-latest": {"id":"codestral-latest","name":"Codestral","tool_call":true,
        "cost":{"input":0.3,"output":0.9},
        "limit":{"context":256000,"output":65536},"modalities":{"input":["text"],"output":["text"]},"status":"active"}
    }
  },
  "cerebras": {
    "id": "cerebras", "name": "Cerebras", "env": ["CEREBRAS_API_KEY"],
    "npm": "@ai-sdk/cerebras", "api": "https://api.cerebras.ai/v1",
    "models": {
      "llama-3.3-70b": {"id":"llama-3.3-70b","name":"Llama 3.3 70B",
        "cost":{"input":0.85,"output":1.2},
        "limit":{"context":131072,"output":16384},"modalities":{"input":["text"],"output":["text"]},"status":"active"}
    }
  },
  "togetherai": {
    "id": "togetherai", "name": "Together AI", "env": ["TOGETHER_API_KEY"],
    "npm": "@ai-sdk/togetherai", "api": "https://api.together.xyz/v1",
    "models": {
      "meta-llama/Llama-3.3-70B-Instruct-Turbo": {"id":"meta-llama/Llama-3.3-70B-Instruct-Turbo","name":"Llama 3.3 70B",
        "cost":{"input":0.88,"output":0.88},
        "limit":{"context":131072,"output":8192},"modalities":{"input":["text"],"output":["text"]},"status":"active"}
    }
  },
  "fireworks": {
    "id": "fireworks", "name": "Fireworks AI", "env": ["FIREWORKS_API_KEY"],
    "npm": "@ai-sdk/fireworks", "api": "https://api.fireworks.ai/inference/v1",
    "models": {
      "accounts/fireworks/models/llama-v3p3-70b-instruct": {"id":"accounts/fireworks/models/llama-v3p3-70b-instruct","name":"Llama 3.3 70B",
        "cost":{"input":0.9,"output":0.9},
        "limit":{"context":131072,"output":16384},"modalities":{"input":["text"],"output":["text"]},"status":"active"}
    }
  },
  "deepinfra": {
    "id": "deepinfra", "name": "DeepInfra", "env": ["DEEPINFRA_API_KEY"],
    "npm": "@ai-sdk/deepinfra", "api": "https://api.deepinfra.com/v1/openai",
    "models": {
      "meta-llama/Llama-3.3-70B-Instruct": {"id":"meta-llama/Llama-3.3-70B-Instruct","name":"Llama 3.3 70B",
        "cost":{"input":0.23,"output":0.4},
        "limit":{"context":131072,"output":16384},"modalities":{"input":["text"],"output":["text"]},"status":"active"}
    }
  },
  "opencode-go": {
    "id": "opencode-go", "name": "OpenCode Zen (Go)", "env": ["OPENCODE_API_KEY"],
    "npm": "@ai-sdk/openai-compatible", "api": "https://opencode.ai/zen/v1",
    "models": {
      "opencode-go": {"id":"opencode-go","name":"OpenCode Go","reasoning":true,"tool_call":true,
        "cost":{"input":0,"output":0},
        "limit":{"context":400000,"output":32768},"modalities":{"input":["text"],"output":["text"]},"status":"active"}
    }
  },
  "hyper": {
    "id": "hyper", "name": "Hyper", "env": ["HYPER_API_KEY"],
    "npm": "@ai-sdk/openai-compatible", "api": "https://hyper.charm.sh/v1",
    "models": {
      "hyper-chat": {"id":"hyper-chat","name":"Hyper Chat","tool_call":true,
        "cost":{"input":0.5,"output":2},
        "limit":{"context":128000,"output":8192},"modalities":{"input":["text"],"output":["text"]},"status":"active"}
    }
  },
  "ollama": {"id":"ollama","name":"Ollama","npm":"@ai-sdk/openai-compatible","api":"http://localhost:11434/v1","models":{}},
  "llamacpp": {"id":"llamacpp","name":"llama.cpp","npm":"@ai-sdk/openai-compatible","api":"http://localhost:8080/v1","models":{}},
  "lmstudio": {"id":"lmstudio","name":"LM Studio","npm":"@ai-sdk/openai-compatible","api":"http://localhost:1234/v1","models":{}},
  "litellm": {"id":"litellm","name":"LiteLLM","npm":"@ai-sdk/openai-compatible","api":"http://localhost:4000/v1","models":{}}
}`

// coreCatalog returns the parsed embedded snapshot.
func coreCatalog() (map[string]CatalogProvider, error) {
	return ParseCatalog([]byte(coreCatalogJSON))
}

// isLocalProvider reports whether the catalog entry needs no API key.
func isLocalProvider(p CatalogProvider) bool {
	switch p.ID {
	case "ollama", "llamacpp", "lmstudio", "litellm":
		return true
	}
	return false
}
