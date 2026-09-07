// Command e2e exercises the SDK against a running crush server: it
// checks health, lists workspaces, and creates a workspace plus a
// session, then prints a summary.
//
// Usage: go run ./examples/e2e [-host unix:///path/to.sock] [-path /project/dir]
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/roberChen/crush-sdk"
	clientproto "github.com/roberChen/crush-sdk/proto"
)

func main() {
	host := flag.String("host", "", "server host URL, e.g. unix:///tmp/crush-e2e.sock; defaults to the standard per-user socket")
	path := flag.String("path", "/tmp", "workspace filesystem path")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var (
		c   *client.Client
		err error
	)
	if *host == "" {
		c, err = client.DefaultClient(*path)
	} else {
		u, perr := parseHost(*host)
		if perr != nil {
			log.Fatalf("invalid host: %v", perr)
		}
		c, err = client.NewClient(*path, u, hostOnly(*host))
	}
	if err != nil {
		log.Fatalf("dial: %v", err)
	}

	if err := c.Health(ctx); err != nil {
		log.Fatalf("health: %v", err)
	}
	fmt.Println("health: ok")

	ws, err := c.ListWorkspaces(ctx)
	if err != nil {
		log.Fatalf("list workspaces: %v", err)
	}
	fmt.Printf("workspaces: %d\n", len(ws))

	created, err := c.CreateWorkspace(ctx, clientproto.Workspace{Path: *path})
	if err != nil {
		log.Fatalf("create workspace: %v", err)
	}
	fmt.Printf("workspace: %s\n", created.ID)

	sess, err := c.CreateSession(ctx, created.ID, "sdk e2e")
	if err != nil {
		log.Fatalf("create session: %v", err)
	}
	fmt.Printf("session: %s\n", sess.ID)
}

func parseHost(host string) (scheme string, err error) {
	for _, s := range []string{"unix", "tcp", "npipe"} {
		if len(host) > len(s)+3 && host[:len(s)+3] == s+"://" {
			return s, nil
		}
	}
	return "", fmt.Errorf("unsupported scheme in %q", host)
}

func hostOnly(host string) string {
	for _, s := range []string{"unix", "tcp", "npipe"} {
		if len(host) > len(s)+3 && host[:len(s)+3] == s+"://" {
			return host[len(s)+3:]
		}
	}
	return host
}
