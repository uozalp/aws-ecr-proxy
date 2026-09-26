// Command server runs the ECR VS Code compatibility proxy: it serves the
// Docker Registry /v2/_catalog endpoint from AWS ECR's DescribeRepositories
// API, and forwards all other Docker Registry requests to the configured
// ECR registry.
package main

import (
	"context"
	"log"
	"net/http"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecr"

	"github.com/uozalp/aws-ecr-proxy/internal/catalog"
	"github.com/uozalp/aws-ecr-proxy/internal/config"
	"github.com/uozalp/aws-ecr-proxy/internal/ecrauth"
	"github.com/uozalp/aws-ecr-proxy/internal/proxy"
	"github.com/uozalp/aws-ecr-proxy/internal/server"
	"github.com/uozalp/aws-ecr-proxy/internal/tagscache"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(cfg.AWSRegion))
	if err != nil {
		log.Fatalf("failed to load AWS credentials: %v", err)
	}

	ecrClient := ecr.NewFromConfig(awsCfg)
	cat := catalog.New(ecrClient, cfg.CatalogCache, "")
	tokens := ecrauth.New(ecrClient)
	tagsHTTPClient := &http.Client{Transport: proxy.NewUpstreamTransport()}
	tags := tagscache.New(cfg.ECRRegistry, tokens, cfg.CatalogCache, tagsHTTPClient)
	ecrProxy := proxy.NewReverseProxy(cfg.ECRRegistry, tokens)
	handler := server.New(cat, tags, ecrProxy)

	log.Printf("listening on :%s, forwarding to %s", cfg.Port, cfg.ECRRegistry)
	if err := http.ListenAndServe(":"+cfg.Port, handler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
