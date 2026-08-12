package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// loginLimiter throttles brute-force attempts on the public login endpoint.
// Keyed by client IP + username: 5 consecutive failures lock that pair out
// for 15 minutes; any success clears it. In-memory is fine — a backend
// restart resetting counters is harmless for this threat model.
type loginLimiter struct {
	mu       sync.Mutex
	fails    map[string]int
	lockedTo map[string]time.Time
}

const (
	loginMaxFails    = 5
	loginLockoutTime = 15 * time.Minute
)

var loginLimits = &loginLimiter{
	fails:    map[string]int{},
	lockedTo: map[string]time.Time{},
}

func (l *loginLimiter) isLocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	until, ok := l.lockedTo[key]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(l.lockedTo, key)
		delete(l.fails, key)
		return false
	}
	return true
}

func (l *loginLimiter) recordFailure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[key]++
	if l.fails[key] >= loginMaxFails {
		l.lockedTo[key] = time.Now().Add(loginLockoutTime)
	}
}

func (l *loginLimiter) recordSuccess(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
	delete(l.lockedTo, key)
}

// clientIP relies on chi middleware.RealIP having already rewritten
// RemoteAddr from X-Forwarded-For (Caddy fronts this service).
func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

type LoginResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	User      struct {
		ID       int    `json:"id"`
		Username string `json:"username"`
		Role     string `json:"role"`
	} `json:"user"`
}

type RegisterRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// handleLogin authenticates a user and returns a JWT token
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}

	if req.Username == "" || req.Password == "" {
		http.Error(w, `{"error":"username and password required"}`, http.StatusBadRequest)
		return
	}

	limitKey := clientIP(r) + "|" + req.Username
	if loginLimits.isLocked(limitKey) {
		http.Error(w, `{"error":"terlalu banyak percobaan login, coba lagi dalam 15 menit"}`, http.StatusTooManyRequests)
		return
	}

	// Lookup user
	var id int
	var hash, role string
	err := s.pg.QueryRow(r.Context(),
		"SELECT id, password_hash, role FROM users WHERE username = $1", req.Username,
	).Scan(&id, &hash, &role)
	if err != nil {
		loginLimits.recordFailure(limitKey)
		http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
		return
	}

	// Verify password
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		loginLimits.recordFailure(limitKey)
		http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
		return
	}
	loginLimits.recordSuccess(limitKey)

	// Generate JWT
	expiresAt := time.Now().Add(24 * time.Hour)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id":  id,
		"username": req.Username,
		"role":     role,
		"exp":      expiresAt.Unix(),
		"iat":      time.Now().Unix(),
	})

	tokenStr, err := token.SignedString([]byte(s.cfg.JWTSecret))
	if err != nil {
		http.Error(w, `{"error":"failed to generate token"}`, http.StatusInternalServerError)
		return
	}

	resp := LoginResponse{
		Token:     tokenStr,
		ExpiresAt: expiresAt.Unix(),
	}
	resp.User.ID = id
	resp.User.Username = req.Username
	resp.User.Role = role

	writeJSON(w, resp)
}

// handleRegister creates a new user (admin only, or first user)
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}

	if req.Username == "" || req.Password == "" {
		http.Error(w, `{"error":"username and password required"}`, http.StatusBadRequest)
		return
	}
	if len(req.Password) < 8 {
		http.Error(w, `{"error":"password must be at least 8 characters"}`, http.StatusBadRequest)
		return
	}

	// Check if this is the first user (auto-admin)
	var userCount int
	s.pg.QueryRow(r.Context(), "SELECT count(*) FROM users").Scan(&userCount)

	role := "viewer"
	if userCount == 0 {
		role = "admin" // First user is always admin
	} else {
		// Only admins can create new users
		claims := getClaimsFromContext(r.Context())
		if claims == nil || claims["role"] != "admin" {
			http.Error(w, `{"error":"admin access required"}`, http.StatusForbidden)
			return
		}
		if req.Role == "admin" || req.Role == "viewer" {
			role = req.Role
		}
	}

	// Hash password
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, `{"error":"failed to hash password"}`, http.StatusInternalServerError)
		return
	}

	var id int
	err = s.pg.QueryRow(r.Context(),
		"INSERT INTO users (username, password_hash, role) VALUES ($1, $2, $3) RETURNING id",
		req.Username, string(hash), role,
	).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "unique") {
			http.Error(w, `{"error":"username already exists"}`, http.StatusConflict)
		} else {
			http.Error(w, `{"error":"failed to create user"}`, http.StatusInternalServerError)
		}
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]interface{}{
		"id":       id,
		"username": req.Username,
		"role":     role,
		"message":  "user created",
	})
}

// handleMe returns the current authenticated user info
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	claims := getClaimsFromContext(r.Context())
	if claims == nil {
		http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
		return
	}
	writeJSON(w, map[string]interface{}{
		"id":       claims["user_id"],
		"username": claims["username"],
		"role":     claims["role"],
	})
}

// handleChangePassword allows user to change their own password
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	claims := getClaimsFromContext(r.Context())
	if claims == nil {
		http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
		return
	}

	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	if len(req.NewPassword) < 8 {
		http.Error(w, `{"error":"new password must be at least 8 characters"}`, http.StatusBadRequest)
		return
	}

	userID := claims["user_id"]

	// Verify old password
	var hash string
	err := s.pg.QueryRow(r.Context(), "SELECT password_hash FROM users WHERE id = $1", userID).Scan(&hash)
	if err != nil {
		http.Error(w, `{"error":"user not found"}`, http.StatusNotFound)
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.OldPassword)); err != nil {
		http.Error(w, `{"error":"old password incorrect"}`, http.StatusUnauthorized)
		return
	}

	newHash, _ := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	s.pg.Exec(r.Context(), "UPDATE users SET password_hash = $1 WHERE id = $2", string(newHash), userID)

	writeJSON(w, map[string]string{"message": "password changed"})
}

// handleAuthCheck returns whether setup is needed (no users yet)
func (s *Server) handleAuthCheck(w http.ResponseWriter, r *http.Request) {
	var count int
	s.pg.QueryRow(r.Context(), "SELECT count(*) FROM users").Scan(&count)
	writeJSON(w, map[string]interface{}{
		"has_users":    count > 0,
		"setup_needed": count == 0,
	})
}

// --- JWT Middleware ---

type contextKey string

const claimsKey contextKey = "claims"

// authMiddleware validates JWT token from Authorization header or ?token= query param
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var tokenStr string

		// Try Authorization header first
		authHeader := r.Header.Get("Authorization")
		if authHeader != "" {
			tokenStr = strings.TrimPrefix(authHeader, "Bearer ")
			if tokenStr == authHeader {
				http.Error(w, `{"error":"invalid authorization format"}`, http.StatusUnauthorized)
				return
			}
		}

		// Fall back to query parameter (for WebSocket)
		if tokenStr == "" {
			tokenStr = r.URL.Query().Get("token")
		}

		if tokenStr == "" {
			http.Error(w, `{"error":"authorization required"}`, http.StatusUnauthorized)
			return
		}

		token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
			return []byte(s.cfg.JWTSecret), nil
		})
		if err != nil || !token.Valid {
			http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
			return
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			http.Error(w, `{"error":"invalid token claims"}`, http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), claimsKey, map[string]interface{}(claims))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func getClaimsFromContext(ctx context.Context) map[string]interface{} {
	claims, _ := ctx.Value(claimsKey).(map[string]interface{})
	return claims
}

// adminMiddleware restricts access to admin-role users only
func (s *Server) adminMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := getClaimsFromContext(r.Context())
		if claims == nil || claims["role"] != "admin" {
			http.Error(w, `{"error":"admin access required"}`, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// generateSetupToken creates a one-time setup token for first user registration
func generateSetupToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
