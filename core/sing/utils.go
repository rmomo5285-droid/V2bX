package sing

import (
	"fmt"
	"strconv"

	"github.com/InazumaV/V2bX/conf"
	"github.com/sagernet/sing-box/option"
)

func processFallback(c *conf.Options, fallbackForALPN map[string]*option.ServerOptions) error {
	for k, v := range c.SingOptions.FallBackConfigs.FallBackForALPN {
		fallbackPort, err := parseServerPort(v.ServerPort)
		if err != nil {
			return fmt.Errorf("unable to parse fallbackForALPN server port error: %s", err)
		}
		fallbackForALPN[k] = &option.ServerOptions{Server: v.Server, ServerPort: fallbackPort}
	}
	return nil
}

func parseServerPort(value string) (uint16, error) {
	port, err := strconv.ParseUint(value, 10, 16)
	if err != nil || port == 0 {
		return 0, fmt.Errorf("invalid server port %q: expected 1..65535", value)
	}
	return uint16(port), nil
}
