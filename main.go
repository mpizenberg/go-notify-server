package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	// Handle "generate-vapid" subcommand.
	if len(os.Args) > 1 && os.Args[1] == "generate-vapid" {
		pub, priv, err := GenerateVAPIDKeys()
		if err != nil {
			log.Fatalf("failed to generate VAPID keys: %v", err)
		}
		fmt.Printf("VAPID_PUBLIC_KEY=%s\n", pub)
		fmt.Printf("VAPID_PRIVATE_KEY=%s\n", priv)
		return
	}

	// Load configuration from environment variables.
	vapidPublicKey := os.Getenv("VAPID_PUBLIC_KEY")
	vapidPrivateKey := os.Getenv("VAPID_PRIVATE_KEY")
	vapidContact := os.Getenv("VAPID_CONTACT")
	adminKey := os.Getenv("ADMIN_KEY")
	dbPath := os.Getenv("DB_PATH")
	port := os.Getenv("PORT")
	corsOrigin := os.Getenv("CORS_ORIGIN")

	// Defaults.
	if dbPath == "" {
		dbPath = "./data/notify.db"
	}
	if port == "" {
		port = "8080"
	}
	if corsOrigin == "" {
		corsOrigin = "*"
	}

	// Validate required env vars.
	if vapidPublicKey == "" || vapidPrivateKey == "" {
		log.Fatal("VAPID_PUBLIC_KEY and VAPID_PRIVATE_KEY are required. Run 'go-notify-server generate-vapid' to generate a keypair.")
	}
	if vapidContact == "" {
		log.Fatal("VAPID_CONTACT is required (e.g. mailto:admin@example.com)")
	}
	if adminKey == "" {
		log.Fatal("ADMIN_KEY is required")
	}

	// Parse VAPID keys to validate them.
	if _, err := ParseVAPIDKeys(vapidPublicKey, vapidPrivateKey); err != nil {
		log.Fatalf("invalid VAPID keys: %v", err)
	}

	// Open database.
	db, err := OpenDB(dbPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Build server.
	srv := &Server{
		DB:              db,
		VAPIDPublicKey:  vapidPublicKey,
		VAPIDPrivateKey: vapidPrivateKey,
		VAPIDContact:    vapidContact,
		AdminKey:        adminKey,
	}

	httpServer := &http.Server{
		Addr:    ":" + port,
		Handler: srv.NewRouter(corsOrigin),
	}

	// Start listening in a goroutine.
	go func() {
		log.Printf("listening on :%s", port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server error: %v", err)
		}
	}()

	// Wait for shutdown signal.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit
	log.Printf("received %s, shutting down...", sig)

	// Stop accepting new connections.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("http server shutdown error: %v", err)
	}

	// Wait for in-flight notification deliveries.
	log.Println("waiting for in-flight notifications...")
	srv.WG.Wait()

	log.Println("shutdown complete")
}
