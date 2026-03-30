package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"sync"

	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/resource"
	generic "go.viam.com/rdk/services/generic"

	"github.com/erh/vmodutils"

	viamchess "viamchess"
)

type moveRecord struct {
	Type  string `json:"type"`
	Label string `json:"label"`
}

type srv struct {
	chess   resource.Resource
	mu      sync.Mutex
	history []moveRecord
	logger  logging.Logger
}

func (s *srv) doCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	res, err := s.chess.DoCommand(ctx, cmd)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return map[string]interface{}{}, nil
	}
	return res, nil
}

func (s *srv) handleState(w http.ResponseWriter, r *http.Request) {
	res, err := s.doCommand(r.Context(), map[string]interface{}{"info": true})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	res["history"] = s.history
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

func (s *srv) handleGo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		N int `json:"n"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.N == 0 {
		req.N = 1
	}
	res, err := s.doCommand(r.Context(), map[string]interface{}{"go": req.N})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	move, _ := res["move"].(string)
	s.mu.Lock()
	s.history = append(s.history, moveRecord{Type: "go", Label: fmt.Sprintf("×%d%s", req.N, ifStr(move != "", " → "+move, ""))})
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

func (s *srv) handleMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
		N    int    `json:"n"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.N == 0 {
		req.N = 1
	}
	_, err := s.doCommand(r.Context(), map[string]interface{}{
		"move": map[string]interface{}{"from": req.From, "to": req.To, "n": req.N},
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	s.history = append(s.history, moveRecord{Type: "move", Label: fmt.Sprintf("%s → %s%s", req.From, req.To, ifStr(req.N > 1, fmt.Sprintf(" ×%d", req.N), ""))})
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (s *srv) simple(cmd map[string]interface{}, label string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.doCommand(r.Context(), cmd); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.mu.Lock()
		s.history = append(s.history, moveRecord{Type: label, Label: label})
		s.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}
}

func ifStr(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

func main() {
	host := flag.String("host", "", "robot host (required)")
	port := flag.String("port", "8080", "local HTTP port")
	pieceFinder := flag.String("piece-finder", "piece-finder", "piece-finder service name")
	arm := flag.String("arm", "arm", "arm component name")
	gripper := flag.String("gripper", "gripper", "gripper component name")
	poseStart := flag.String("pose-start", "hack-pose-look-straight-down", "pose-start switch name")
	camera := flag.String("camera", "cam", "camera component name")
	flag.Parse()

	if *host == "" {
		panic("need -host")
	}

	ctx := context.Background()
	logger := logging.NewLogger("webapp")

	machine, err := vmodutils.ConnectToHostFromCLIToken(ctx, *host, logger)
	if err != nil {
		panic(err)
	}
	defer machine.Close(ctx)

	deps, err := vmodutils.MachineToDependencies(machine)
	if err != nil {
		panic(err)
	}

	cfg := viamchess.ChessConfig{
		PieceFinder: *pieceFinder,
		Arm:         *arm,
		Gripper:     *gripper,
		PoseStart:   *poseStart,
		Camera:      *camera,
	}
	if _, _, err = cfg.Validate(""); err != nil {
		panic(err)
	}

	chess, err := viamchess.NewChess(ctx, deps, generic.Named("webapp"), &cfg, logger)
	if err != nil {
		panic(err)
	}
	defer chess.(interface{ Close(context.Context) error }).Close(ctx)

	s := &srv{chess: chess, logger: logger, history: []moveRecord{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/go", s.handleGo)
	mux.HandleFunc("/api/move", s.handleMove)
	mux.HandleFunc("/api/reset", s.simple(map[string]interface{}{"reset": true}, "reset"))
	mux.HandleFunc("/api/wipe", s.simple(map[string]interface{}{"wipe": true}, "wipe"))
	mux.HandleFunc("/api/clear-cache", s.simple(map[string]interface{}{"ClearCache": true}, "cache"))
	mux.Handle("/", http.FileServer(http.Dir("cmd/webapp/static")))

	addr := ":" + *port
	logger.Infof("open http://localhost%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		panic(err)
	}
}
