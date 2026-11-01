package main

import (
	"fmt"
	"log"
	"os"

	"github.com/raykavin/xtrader/download"
	"github.com/raykavin/xtrader/exchange"

	"github.com/urfave/cli/v2"
)

func main() {
	app := &cli.App{
		Name:     "xtrader",
		HelpName: "xtrader",
		Usage:    "Utilities for bot creation",
		Commands: []*cli.Command{
			seasonalityCommand,
			metalabelCommand,
			{
				Name:     "download",
				HelpName: "download",
				Usage:    "Download historical data",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "pair",
						Aliases:  []string{"p"},
						Usage:    "eg. XAUUSDm",
						Required: true,
					},
					&cli.IntFlag{
						Name:     "days",
						Aliases:  []string{"d"},
						Usage:    "eg. 100 (default 30 days)",
						Required: false,
					},
					&cli.TimestampFlag{
						Name:     "start",
						Aliases:  []string{"s"},
						Usage:    "eg. 2021-12-01",
						Layout:   "2006-01-02",
						Required: false,
					},
					&cli.TimestampFlag{
						Name:     "end",
						Aliases:  []string{"e"},
						Usage:    "eg. 2020-12-31",
						Layout:   "2006-01-02",
						Required: false,
					},
					&cli.StringFlag{
						Name:     "timeframe",
						Aliases:  []string{"t"},
						Usage:    "eg. 1h",
						Required: true,
					},
					&cli.StringFlag{
						Name:     "output",
						Aliases:  []string{"o"},
						Usage:    "eg. ./xau-1h.csv",
						Required: true,
					},
					&cli.StringFlag{
						Name: "exchange",
						Usage: "mt5, the terminal of the account through the bridge of --mt5-bridge " +
							"(tools/mt5bridge): the candles are bid prices",
						Value: "mt5",
					},
					&cli.StringFlag{
						Name:  "mt5-bridge",
						Usage: "mt5: address of the bridge running with the terminal",
						Value: exchange.MT5DefaultBaseURL,
					},
					&cli.StringFlag{
						Name:    "mt5-token",
						Usage:   "mt5: token of the bridge, its --token",
						EnvVars: []string{"MT5_BRIDGE_TOKEN"},
					},
					&cli.BoolFlag{
						Name:  "mt5-live",
						Usage: "mt5: allow a real account, refused otherwise",
					},
				},
				Action: func(c *cli.Context) error {
					if name := c.String("exchange"); name != "mt5" {
						return fmt.Errorf("invalid exchange %q: mt5", name)
					}
					// fetch bid candles from the terminal through the bridge; data only: a
					// download never trades nor touches the positions of the account
					options := []exchange.MT5Option{
						exchange.WithMT5BaseURL(c.String("mt5-bridge")),
						exchange.WithMT5Symbols(c.String("pair")),
						exchange.WithMT5DataOnly(),
					}
					if token := c.String("mt5-token"); token != "" {
						options = append(options, exchange.WithMT5Token(token))
					}
					if c.Bool("mt5-live") {
						options = append(options, exchange.WithMT5Real())
					}
					exc, err := exchange.NewMT5(c.Context, options...)
					if err != nil {
						return err
					}

					var downloads []download.Option
					if days := c.Int("days"); days > 0 {
						downloads = append(downloads, download.WithDays(days))
					}

					start := c.Timestamp("start")
					end := c.Timestamp("end")
					if start != nil && end != nil && !start.IsZero() && !end.IsZero() {
						downloads = append(downloads, download.WithInterval(*start, *end))
					} else if start != nil || end != nil {
						log.Fatal("START and END must be informed together")
					}

					return download.NewDownloader(exc).Download(c.Context, c.String("pair"),
						c.String("timeframe"), c.String("output"), downloads...)

				},
			},
		},
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}
