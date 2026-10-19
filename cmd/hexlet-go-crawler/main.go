package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"code/crawler"

	"github.com/urfave/cli/v2"
)

func main() {
	app := newApp()
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}

func newApp() *cli.App {
	return &cli.App{
		Name:  "hexlet-go-crawler",
		Usage: "analyze a website structure",
		Flags: []cli.Flag{
			&cli.IntFlag{Name: "depth", Value: 10, Usage: "crawl depth"},
			&cli.IntFlag{Name: "retries", Value: 1, Usage: "number of retries for failed requests"},
			&cli.DurationFlag{Name: "delay", Usage: "delay between requests (example: 200ms, 1s)"},
			&cli.DurationFlag{Name: "timeout", Value: 15 * time.Second, Usage: "per-request timeout"},
			&cli.Float64Flag{Name: "rps", Usage: "limit requests per second (overrides delay)"},
			&cli.StringFlag{Name: "user-agent", Usage: "custom user agent"},
			&cli.IntFlag{Name: "workers", Value: 4, Usage: "number of concurrent workers"},
		},
		Action: run,
	}
}

func run(cliCtx *cli.Context) error {
	if cliCtx.Args().Len() == 0 {
		fmt.Fprintln(os.Stderr, "url is required")
		return cli.ShowAppHelp(cliCtx)
	}

	report, err := crawler.Analyze(context.Background(), crawler.Options{
		URL:         cliCtx.Args().First(),
		Depth:       cliCtx.Int("depth"),
		Retries:     cliCtx.Int("retries"),
		Delay:       cliCtx.Duration("delay"),
		Timeout:     cliCtx.Duration("timeout"),
		RPS:         cliCtx.Float64("rps"),
		UserAgent:   cliCtx.String("user-agent"),
		Concurrency: cliCtx.Int("workers"),
		IndentJSON:  true,
		HTTPClient:  &http.Client{},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "analysis failed: %v\n", err)
		return nil
	}

	return writeReport(os.Stdout, report)
}

func writeReport(output io.Writer, report []byte) error {
	if _, err := output.Write(report); err != nil {
		return err
	}

	_, err := output.Write([]byte("\n"))

	return err
}
