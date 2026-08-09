package server

import (
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mpanel/internal/auth"
	configmanager "mpanel/internal/config"
	"mpanel/internal/share"
)

type Server struct {
	auth   *auth.Manager
	config *configmanager.Manager
	web    fs.FS
	logger *slog.Logger
}

//go:embed web/*
var webFiles embed.FS

func New(authManager *auth.Manager, configManager *configmanager.Manager, logger *slog.Logger) *Server {
	web, _ := fs.Sub(webFiles, "web")
	return &Server{auth: authManager, config: configManager, web: web, logger: logger}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.Handle("POST /api/auth/login", s.mutation(http.HandlerFunc(s.login)))
	mux.Handle("GET /api/auth/session", s.protected(http.HandlerFunc(s.session)))
	mux.Handle("POST /api/auth/logout", s.protected(s.mutation(http.HandlerFunc(s.logout))))
	mux.Handle("GET /api/config", s.protected(http.HandlerFunc(s.getConfig)))
	mux.Handle("PUT /api/config", s.protected(s.mutation(http.HandlerFunc(s.putConfig))))
	mux.Handle("GET /api/config/backups", s.protected(http.HandlerFunc(s.backups)))
	mux.Handle("POST /api/config/backups/{id}/restore", s.protected(s.mutation(http.HandlerFunc(s.restore))))
	mux.Handle("GET /api/listeners", s.protected(http.HandlerFunc(s.listeners)))
	mux.Handle("POST /api/listeners", s.protected(s.mutation(http.HandlerFunc(s.createListener))))
	mux.Handle("PUT /api/listeners/{name}", s.protected(s.mutation(http.HandlerFunc(s.updateListener))))
	mux.Handle("DELETE /api/listeners/{name}", s.protected(s.mutation(http.HandlerFunc(s.deleteListener))))
	mux.Handle("GET /api/listeners/{name}/shares", s.protected(http.HandlerFunc(s.listenerShares)))
	mux.Handle("/", http.HandlerFunc(s.static))
	return securityHeaders(limitBody(mux))
}

func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		}
		next.ServeHTTP(w, r)
	})
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'; img-src 'self' data:")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) protected(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.auth.Authenticate(r) != nil {
			errorResponse(w, http.StatusUnauthorized, "未登录或会话已过期")
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) mutation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r) {
			errorResponse(w, http.StatusForbidden, "拒绝跨站请求")
			return
		}
		// DELETE and bodyless POST need no Content-Type.
		if r.Method != http.MethodDelete && r.ContentLength != 0 {
			media := strings.ToLower(strings.Split(r.Header.Get("Content-Type"), ";")[0])
			if media != "application/json" {
				errorResponse(w, http.StatusUnsupportedMediaType, "请求必须使用 application/json")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(origin string, r *http.Request) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	scheme := "http"
	if auth.IsSecure(r) {
		scheme = "https"
	}
	return strings.EqualFold(u.Scheme, scheme) && sameHost(u.Host, r.Host)
}
func sameHost(a, b string) bool {
	ah, ap := splitHost(a)
	bh, bp := splitHost(b)
	return strings.EqualFold(ah, bh) && ap == bp
}
func splitHost(hostport string) (string, string) {
	host, port, err := net.SplitHostPort(hostport)
	if err == nil {
		return host, port
	}
	return strings.TrimSuffix(hostport, "."), ""
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !s.auth.ValidCredentials(input.Username, input.Password) {
		time.Sleep(150 * time.Millisecond)
		errorResponse(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	s.auth.SetCookie(w, auth.IsSecure(r))
	jsonResponse(w, http.StatusOK, map[string]bool{"authenticated": true})
}
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, 200, map[string]bool{"authenticated": true})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.auth.ClearCookie(w, auth.IsSecure(r))
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	data, err := s.config.Read()
	if err != nil {
		errorResponse(w, 500, "读取配置失败")
		return
	}
	jsonResponse(w, 200, map[string]string{"yaml": string(data)})
}
func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	var input struct {
		YAML string `json:"yaml"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if err := s.config.Save(r.Context(), []byte(input.YAML)); err != nil {
		errorResponse(w, 400, safeError(err))
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
func (s *Server) backups(w http.ResponseWriter, r *http.Request) {
	items, err := s.config.Backups()
	if err != nil {
		errorResponse(w, 500, "读取备份失败")
		return
	}
	jsonResponse(w, 200, map[string]any{"backups": items})
}
func (s *Server) restore(w http.ResponseWriter, r *http.Request) {
	if err := s.config.Restore(r.Context(), r.PathValue("id")); err != nil {
		errorResponse(w, 400, safeError(err))
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
func (s *Server) listeners(w http.ResponseWriter, r *http.Request) {
	items, err := s.config.Listeners()
	if err != nil {
		errorResponse(w, 500, safeError(err))
		return
	}
	jsonResponse(w, 200, map[string]any{"listeners": items})
}
func (s *Server) createListener(w http.ResponseWriter, r *http.Request) {
	var item configmanager.Listener
	if !decodeJSON(w, r, &item) {
		return
	}
	if err := s.config.CreateListener(r.Context(), item); err != nil {
		errorResponse(w, 400, safeError(err))
		return
	}
	jsonResponse(w, 201, map[string]bool{"ok": true})
}
func (s *Server) updateListener(w http.ResponseWriter, r *http.Request) {
	var item configmanager.Listener
	if !decodeJSON(w, r, &item) {
		return
	}
	name, err := url.PathUnescape(r.PathValue("name"))
	if err != nil {
		errorResponse(w, 400, "无效的入站名称")
		return
	}
	if err := s.config.UpdateListener(r.Context(), name, item); err != nil {
		errorResponse(w, 400, safeError(err))
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
func (s *Server) deleteListener(w http.ResponseWriter, r *http.Request) {
	name, err := url.PathUnescape(r.PathValue("name"))
	if err != nil {
		errorResponse(w, 400, "无效的入站名称")
		return
	}
	if err := s.config.DeleteListener(r.Context(), name); err != nil {
		errorResponse(w, 400, safeError(err))
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

func (s *Server) listenerShares(w http.ResponseWriter, r *http.Request) {
	name, err := url.PathUnescape(r.PathValue("name"))
	if err != nil {
		errorResponse(w, 400, "无效的入站名称")
		return
	}
	host, err := share.ValidateHost(r.URL.Query().Get("host"))
	if err != nil {
		errorResponse(w, 400, safeError(err))
		return
	}
	items, err := s.config.Listeners()
	if err != nil {
		errorResponse(w, 500, safeError(err))
		return
	}
	var target configmanager.Listener
	found := false
	for _, item := range items {
		if item.Name == name {
			target = item
			found = true
			break
		}
	}
	if !found {
		errorResponse(w, 404, "入站不存在")
		return
	}
	entries, err := share.Build(target, host)
	if err != nil {
		errorResponse(w, 400, safeError(err))
		return
	}
	jsonResponse(w, 200, share.Result{Shares: entries})
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		errorResponse(w, 404, "接口不存在")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	data, err := fs.ReadFile(s.web, path)
	if err != nil {
		data, err = fs.ReadFile(s.web, "index.html")
	}
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	if strings.HasSuffix(path, ".css") {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	} else if strings.HasSuffix(path, ".js") {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	w.Write(data)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		errorResponse(w, 400, "请求内容无效")
		return false
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		errorResponse(w, 400, "请求只能包含一个 JSON 对象")
		return false
	}
	return true
}
func safeError(err error) string {
	message := err.Error()
	if len(message) > 2000 {
		message = message[:2000]
	}
	return message
}
func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func errorResponse(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, map[string]string{"error": message})
}
