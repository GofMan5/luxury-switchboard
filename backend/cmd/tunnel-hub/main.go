package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	hubhttp "github.com/luxuryprivate/switchboard/backend/internal/hub/adapters/http"
	"github.com/luxuryprivate/switchboard/backend/internal/hub/adapters/jsonfile"
	"github.com/luxuryprivate/switchboard/backend/internal/hub/application"
)

var actionPattern = regexp.MustCompile(`^v1 (pause|resume|stop) ([0-9]{1,19}) ([0-9]{1,3})$`)

func main() {
	root := flag.NewFlagSet("tunnel-hub", flag.ContinueOnError)
	root.SetOutput(io.Discard)
	statePath := root.String("state", envOr("TUNNEL_HUB_STATE", "/var/lib/tunnel-hub/tunnels.json"), "")
	if root.Parse(os.Args[1:]) != nil || len(root.Args()) == 0 {
		os.Exit(2)
	}
	service, _ := application.NewService(jsonfile.New(*statePath))
	switch root.Args()[0] {
	case "control":
		os.Exit(control(service, root.Args()[1:]))
	case "serve":
		os.Exit(serve(service, root.Args()[1:]))
	default:
		os.Exit(2)
	}
}

func control(service *application.Service, args []string) int {
	flags := flag.NewFlagSet("control", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	owner := flags.String("owner", "", "")
	if flags.Parse(args) != nil {
		return failure()
	}
	command := os.Getenv("SSH_ORIGINAL_COMMAND")
	var snapshot application.Snapshot
	var err error
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if command == "v1 list" {
		snapshot, err = service.List(ctx, *owner)
	} else if match := actionPattern.FindStringSubmatch(command); len(match) == 4 {
		revision, revisionErr := strconv.ParseInt(match[2], 10, 64)
		position, positionErr := strconv.Atoi(match[3])
		if revisionErr != nil || positionErr != nil {
			err = fmt.Errorf("invalid request")
		} else {
			snapshot, err = service.Control(ctx, *owner, match[1], revision, position)
		}
	} else {
		err = fmt.Errorf("invalid request")
	}
	if err != nil {
		return failure()
	}
	raw, err := json.Marshal(snapshot)
	if err != nil || len(raw) > 64*1024-1 {
		return failure()
	}
	_, _ = os.Stdout.Write(append(raw, '\n'))
	return 0
}

func failure() int {
	_, _ = os.Stdout.Write([]byte(`{"v":1,"ok":false,"error":"request_failed"}` + "\n"))
	return 1
}

func serve(service *application.Service, args []string) int {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	listen := flags.String("listen", "172.20.0.1", "")
	port := flags.Int("port", 18080, "")
	if flags.Parse(args) != nil || *port < 1 || *port > 65535 {
		return 2
	}
	server := &http.Server{Addr: fmt.Sprintf("%s:%d", *listen, *port), Handler: hubhttp.NewGate(service), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 * 1024}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return 1
	}
	return 0
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
