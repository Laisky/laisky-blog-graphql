package askuser

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"strings"
	"time"

	logSDK "github.com/Laisky/go-utils/v5/log"
	"github.com/Laisky/zap"
	"github.com/google/uuid"
)

// NewHTTPHandler builds an HTTP multiplexer exposing the ask_user dashboard and APIs.
func NewHTTPHandler(service *Service, logger logSDK.Logger) http.Handler {
	handler := &httpHandler{
		service: service,
		logger:  logger,
		page:    template.Must(template.New("askuser").Parse(pageHTML)),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", handler.servePage)
	mux.HandleFunc("/api/requests", handler.handleRequests)
	mux.HandleFunc("/api/requests/", handler.handleRequestByID)
	return mux
}

type httpHandler struct {
	service *Service
	logger  logSDK.Logger
	page    *template.Template
}

func (h *httpHandler) log() logSDK.Logger {
	if h.logger != nil {
		return h.logger
	}
	return serviceLogger()
}

func (h *httpHandler) servePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.page.Execute(w, nil); err != nil {
		h.log().Warn("render ask_user page", zap.Error(err))
	}
}

func (h *httpHandler) handleRequests(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listRequests(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *httpHandler) handleRequestByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/requests/")
	if path == "" {
		http.NotFound(w, r)
		return
	}

	id, err := uuid.Parse(path)
	if err != nil {
		http.Error(w, "invalid request id", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPost:
		h.answerRequest(w, r, id)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *httpHandler) listRequests(w http.ResponseWriter, r *http.Request) {
	auth, err := ParseAuthorizationContext(r.Header.Get("Authorization"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	if h.service == nil {
		http.Error(w, "ask_user service unavailable", http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	pending, history, err := h.service.ListRequests(ctx, auth)
	if err != nil {
		h.log().Error("list ask_user requests", zap.Error(err))
		http.Error(w, "failed to load requests", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"pending":  serializeRequests(pending),
		"history":  serializeRequests(history),
		"user_id":  auth.UserIdentity,
		"ai_id":    auth.AIIdentity,
		"key_hint": auth.KeySuffix,
	})
}

func (h *httpHandler) answerRequest(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if h.service == nil {
		http.Error(w, "ask_user service unavailable", http.StatusServiceUnavailable)
		return
	}
	auth, err := ParseAuthorizationContext(r.Header.Get("Authorization"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	payload := struct {
		Answer string `json:"answer"`
	}{}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	payload.Answer = strings.TrimSpace(payload.Answer)
	if payload.Answer == "" {
		http.Error(w, "answer cannot be empty", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	req, err := h.service.AnswerRequest(ctx, auth, id, payload.Answer)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, ErrRequestNotFound):
			status = http.StatusNotFound
		case errors.Is(err, ErrForbidden), errors.Is(err, ErrInvalidAuthorization):
			status = http.StatusForbidden
		}
		h.log().Warn("answer ask_user request", zap.Error(err))
		http.Error(w, err.Error(), status)
		return
	}

	writeJSON(w, map[string]any{
		"request": serializeRequest(*req),
	})
}

func serializeRequests(reqs []Request) []map[string]any {
	items := make([]map[string]any, 0, len(reqs))
	for _, req := range reqs {
		items = append(items, serializeRequest(req))
	}
	return items
}

func serializeRequest(req Request) map[string]any {
	result := map[string]any{
		"id":            req.ID.String(),
		"question":      req.Question,
		"status":        req.Status,
		"created_at":    req.CreatedAt,
		"updated_at":    req.UpdatedAt,
		"ai_identity":   req.AIIdentity,
		"user_identity": req.UserIdentity,
	}
	if req.Answer != nil {
		result["answer"] = *req.Answer
	}
	if req.AnsweredAt != nil {
		result["answered_at"] = req.AnsweredAt
	}
	return result
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}

func serviceLogger() logSDK.Logger {
	return logSDK.Shared.Named("ask_user_http")
}

const pageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>ask_user Console</title>
<style>
    :root { color-scheme: light dark; }
    body { font-family: system-ui, sans-serif; margin: 0; padding: 0; background: #0f172a; color: #e2e8f0; }
    .wrapper { max-width: 960px; margin: 0 auto; padding: 32px 24px 48px; }
    h1 { font-size: 1.8rem; margin-bottom: 8px; }
    h2 { font-size: 1.3rem; margin-top: 32px; }
    p.lead { color: #94a3b8; margin-top: 0; }
    form#auth-form { display: flex; gap: 12px; margin: 24px 0; }
    form#auth-form input { flex: 1; padding: 10px 14px; border-radius: 8px; border: none; background: rgba(15, 23, 42, 0.6); color: inherit; box-shadow: inset 0 0 0 1px rgba(148, 163, 184, 0.4); }
    form#auth-form button { padding: 10px 18px; border-radius: 8px; border: none; background: #38bdf8; color: #0f172a; font-weight: 600; cursor: pointer; }
    form#auth-form button:hover { background: #0ea5e9; }
    .card { background: rgba(15, 23, 42, 0.85); border-radius: 12px; padding: 18px 20px; margin-top: 16px; box-shadow: 0 12px 24px rgba(15, 23, 42, 0.35); }
    .card.pending { border: 1px solid rgba(56, 189, 248, 0.4); }
    .card.answered { border: 1px solid rgba(74, 222, 128, 0.3); }
    .meta { font-size: 0.85rem; color: #94a3b8; margin-bottom: 12px; display: flex; gap: 12px; flex-wrap: wrap; }
    .question { font-size: 1rem; margin-bottom: 12px; white-space: pre-wrap; }
    .answer { font-size: 0.95rem; margin-top: 12px; white-space: pre-wrap; background: rgba(148, 163, 184, 0.12); padding: 12px; border-radius: 8px; }
    .pending .answer-editor textarea { width: 100%; min-height: 96px; padding: 10px; border-radius: 8px; border: none; background: rgba(15, 23, 42, 0.6); color: inherit; box-shadow: inset 0 0 0 1px rgba(148, 163, 184, 0.4); resize: vertical; }
    .pending .answer-editor button { margin-top: 10px; padding: 8px 14px; border-radius: 6px; border: none; background: #22c55e; color: #0f172a; font-weight: 600; cursor: pointer; }
    .pending .answer-editor button:hover { background: #16a34a; }
    #status { margin-top: 12px; font-size: 0.9rem; }
    .hidden { display: none; }
    .history-empty { color: #64748b; font-style: italic; }
</style>
</head>
<body>
<div class="wrapper">
    <h1>ask_user Console</h1>
    <p class="lead">Review pending questions from your AI assistants and respond directly.</p>
    <form id="auth-form">
        <input type="password" id="api-key" placeholder="Enter your API key" autocomplete="off" required />
        <button type="submit">Connect</button>
    </form>
    <div id="status" class="hidden"></div>
    <section>
        <h2>Pending Questions</h2>
        <div id="pending-list"></div>
    </section>
    <section>
        <h2>History</h2>
        <div id="history-list"></div>
    </section>
</div>
<script>
(function() {
    const statusEl = document.getElementById('status');
    const pendingList = document.getElementById('pending-list');
    const historyList = document.getElementById('history-list');
    const form = document.getElementById('auth-form');
    const apiKeyInput = document.getElementById('api-key');
    const STORAGE_KEY = 'ask_user_api_key';
    let apiKey = localStorage.getItem(STORAGE_KEY) || '';
    let pollTimer = null;

    function setStatus(message, isError) {
        if (!message) {
            statusEl.classList.add('hidden');
            statusEl.textContent = '';
            return;
        }
        statusEl.classList.remove('hidden');
        statusEl.textContent = message;
        statusEl.style.color = isError ? '#f87171' : '#22c55e';
    }

    function applyApiKey(key) {
        apiKey = key.trim();
        if (apiKey) {
            localStorage.setItem(STORAGE_KEY, apiKey);
            apiKeyInput.value = '********';
            setStatus('Connected. Fetching requests...', false);
            schedulePoll(0);
        } else {
            localStorage.removeItem(STORAGE_KEY);
            apiKeyInput.value = '';
            pendingList.innerHTML = '';
            historyList.innerHTML = '';
            setStatus('Disconnected.', false);
            stopPolling();
        }
    }

    function stopPolling() {
        if (pollTimer) {
            clearTimeout(pollTimer);
            pollTimer = null;
        }
    }

    function schedulePoll(delay) {
        stopPolling();
        pollTimer = setTimeout(fetchRequests, delay);
    }

    async function fetchRequests() {
        if (!apiKey) {
            return;
        }
        try {
            const response = await fetch('api/requests', {
                headers: { 'Authorization': 'Bearer ' + apiKey }
            });
            if (!response.ok) {
                throw new Error(await response.text() || 'Failed to fetch requests');
            }
            const data = await response.json();
            renderRequests(data.pending || [], data.history || []);
            const identity = (data.user_id || '') + ' / ' + (data.ai_id || '');
            setStatus('Linked identities: ' + identity, false);
            schedulePoll(5000);
        } catch (err) {
            console.error(err);
            setStatus(err.message || 'Failed to fetch requests', true);
            schedulePoll(8000);
        }
    }

    function renderRequests(pending, history) {
        if (!Array.isArray(pending) || pending.length === 0) {
            pendingList.innerHTML = '<p class="history-empty">No pending questions.</p>';
        } else {
            pendingList.innerHTML = pending.map(renderPendingCard).join('');
            Array.from(pendingList.querySelectorAll('form[data-request-id]')).forEach(form => {
                form.addEventListener('submit', onAnswerSubmit);
            });
        }
        if (!Array.isArray(history) || history.length === 0) {
            historyList.innerHTML = '<p class="history-empty">No history yet.</p>';
        } else {
            historyList.innerHTML = history.map(renderHistoryCard).join('');
        }
    }

    function renderPendingCard(req) {
        var html = '';
        html += '<div class="card pending">';
        html += '<div class="meta">';
        html += '<span>ID: ' + req.id + '</span>';
        html += '<span>Asked: ' + formatDate(req.created_at) + '</span>';
        html += '<span>AI: ' + req.ai_identity + '</span>';
        html += '</div>';
        html += '<div class="question">' + escapeHTML(req.question) + '</div>';
        html += '<form class="answer-editor" data-request-id="' + req.id + '">';
        html += '<textarea placeholder="Provide your answer..." required></textarea>';
        html += '<button type="submit">Send answer</button>';
        html += '</form>';
        html += '</div>';
        return html;
    }

    function renderHistoryCard(req) {
        var html = '';
        html += '<div class="card answered">';
        html += '<div class="meta">';
        html += '<span>ID: ' + req.id + '</span>';
        html += '<span>Asked: ' + formatDate(req.created_at) + '</span>';
        html += '<span>Answered: ' + formatDate(req.answered_at) + '</span>';
        html += '</div>';
        html += '<div class="question">' + escapeHTML(req.question) + '</div>';
        if (req.answer) {
            html += '<div class="answer">' + escapeHTML(req.answer) + '</div>';
        } else {
            html += '<div class="answer">No answer provided.</div>';
        }
        html += '</div>';
        return html;
    }

    async function onAnswerSubmit(event) {
        event.preventDefault();
        const formEl = event.currentTarget;
        const textarea = formEl.querySelector('textarea');
        const requestId = formEl.dataset.requestId;
        if (!textarea || !requestId) {
            return;
        }
        const answer = textarea.value.trim();
        if (!answer) {
            return;
        }
        formEl.querySelector('button')?.setAttribute('disabled', 'disabled');
        try {
            const response = await fetch('api/requests/' + requestId, {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                    'Authorization': 'Bearer ' + apiKey
                },
                body: JSON.stringify({ answer: answer })
            });
            if (!response.ok) {
                throw new Error(await response.text() || 'Failed to submit answer');
            }
            textarea.value = '';
            schedulePoll(0);
            setStatus('Answer submitted successfully.', false);
        } catch (err) {
            console.error(err);
            setStatus(err.message || 'Failed to submit answer', true);
        } finally {
            formEl.querySelector('button')?.removeAttribute('disabled');
        }
    }

    function formatDate(input) {
        if (!input) {
            return 'N/A';
        }
        const date = new Date(input);
        if (Number.isNaN(date.getTime())) {
            return String(input);
        }
        return date.toLocaleString();
    }

    function escapeHTML(value) {
        return String(value || '').replace(/[&<>"']/g, function (c) {
            return ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c];
        });
    }

    form.addEventListener('submit', function(event) {
        event.preventDefault();
        applyApiKey(apiKeyInput.value);
    });

    if (apiKey) {
        applyApiKey(apiKey);
    }
})();
</script>
</body>
</html>`
