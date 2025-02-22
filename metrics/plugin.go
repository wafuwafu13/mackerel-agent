package metrics

import (
	"bufio"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/mackerelio/golib/logging"
	"github.com/mackerelio/mackerel-agent/config"
	mkr "github.com/mackerelio/mackerel-client-go"
)

// pluginGenerator collects user-defined metrics.
// mackerel-agent runs specified command and parses the result for the metric names and values.
type pluginGenerator struct {
	Config *config.MetricPlugin
	Meta   *pluginMeta
}

// pluginMeta is generated from plugin command. (not the configuration file)
type pluginMeta struct {
	Graphs map[string]customGraphDef
}

type customGraphDef struct {
	Label   string
	Unit    string
	Metrics []customGraphMetricDef
}

type customGraphMetricDef struct {
	Name    string
	Label   string
	Stacked bool
}

var pluginLogger = logging.GetLogger("metrics.plugin")

const pluginPrefix = "custom."

var pluginConfigurationEnvName = "MACKEREL_AGENT_PLUGIN_META"

// NewPluginGenerator XXX
func NewPluginGenerator(conf *config.MetricPlugin) PluginGenerator {
	return &pluginGenerator{Config: conf}
}

func (g *pluginGenerator) Generate() (Values, error) {
	results, err := g.collectValues()
	if err != nil {
		return nil, err
	}
	return results, nil
}

func (g *pluginGenerator) PrepareGraphDefs() ([]*mkr.GraphDefsParam, error) {
	err := g.loadPluginMeta()
	if err != nil {
		return nil, err
	}

	payload := g.makeGraphDefsParam()
	return payload, nil
}

func (g *pluginGenerator) CustomIdentifier() *string {
	return g.Config.CustomIdentifier
}

var pluginMetaHeadlineReg = regexp.MustCompile(`^#\s*mackerel-agent-plugin\b(.*)`)

// loadPluginMeta obtains plugin information (e.g. graph visuals, metric
// namespaces, etc) from the command specified.
// mackerel-agent runs the command with MACKEREL_AGENT_PLUGIN_META
// environment variable set.  The command is supposed to output like below:
//
// 	# mackerel-agent-plugin
// 	{
// 	  "graphs": {
// 	    GRAPH_NAME: {
// 	      "label": GRAPH_LABEL,
// 	      "unit": UNIT_TYPE
// 	      "metrics": [
// 	        {
// 	          "name": METRIC_NAME,
// 	          "label": METRIC_LABEL
// 	        },
// 	        ...
// 	      ]
// 	    },
// 	    GRAPH_NAME: ...
// 	  }
// 	}
//
// Valid UNIT_TYPEs are: "float", "integer", "percentage", "seconds", "milliseconds", "bytes", "bytes/sec", "bits/sec", "iops"
//
// The output should start with a line beginning with '#', which contains
// meta-info of the configuration. (eg. plugin schema version)
//
// Below is a working example where the plugin emits metrics named "dice.d6" and "dice.d20":
//
// 	{
// 	  "graphs": {
// 	    "dice": {
// 	      "metrics": [
// 	        {
// 	          "name": "d6",
// 	          "label": "Die (d6)"
// 	        },
// 	        {
// 	          "name": "d20",
// 	          "label": "Die (d20)"
// 	        }
// 	      ],
// 	      "unit": "integer",
// 	      "label": "My Dice"
// 	    }
// 	  }
// 	}
func (g *pluginGenerator) loadPluginMeta() error {
	// Set environment variable to make the plugin command generate its configuration
	pluginMetaEnv := pluginConfigurationEnvName + "=1"
	stdout, stderr, exitCode, err := g.Config.Command.RunWithEnv([]string{pluginMetaEnv})
	if err != nil {
		return fmt.Errorf("running %s failed: %s, exit=%d stderr=%q", g.Config.Command.CommandString(), err, exitCode, stderr)
	}

	outBuffer := bufio.NewReader(strings.NewReader(stdout))
	// Read the plugin configuration meta (version etc)

	headerLine, err := outBuffer.ReadString('\n')
	if err != nil {
		return fmt.Errorf("while reading the first line of command %s: %s", g.Config.Command.CommandString(), err)
	}

	// Parse the header line of format:
	// # mackerel-agent-plugin [key=value]...
	pluginMetaHeader := map[string]string{}

	m := pluginMetaHeadlineReg.FindStringSubmatch(headerLine)
	if m == nil {
		return fmt.Errorf("bad format of first line: %q", headerLine)
	}

	for _, field := range strings.Fields(m[1]) {
		keyValue := strings.Split(field, "=")
		var value string
		if len(keyValue) > 1 {
			value = keyValue[1]
		} else {
			value = ""
		}
		pluginMetaHeader[keyValue[0]] = value
	}

	// Check schema version
	version, ok := pluginMetaHeader["version"]
	if !ok {
		version = "1"
	}

	if version != "1" {
		return fmt.Errorf("unsupported plugin meta version: %q", version)
	}

	conf := &pluginMeta{}
	err = json.NewDecoder(outBuffer).Decode(conf)

	if err != nil {
		return fmt.Errorf("while reading plugin configuration: %s", err)
	}

	g.Meta = conf

	return nil
}

func (g *pluginGenerator) makeGraphDefsParam() []*mkr.GraphDefsParam {
	return makeGraphDefsParam(g.Meta)
}

func makeGraphDefsParam(meta *pluginMeta) []*mkr.GraphDefsParam {
	if meta == nil {
		return nil
	}

	var payloads []*mkr.GraphDefsParam
	for key, graph := range meta.Graphs {
		payload := &mkr.GraphDefsParam{
			Name:        pluginPrefix + key,
			DisplayName: graph.Label,
			Unit:        graph.Unit,
		}
		if payload.Unit == "" {
			payload.Unit = "float"
		}

		for _, metric := range graph.Metrics {
			metricPayload := &mkr.GraphDefsMetric{
				Name:        pluginPrefix + key + "." + metric.Name,
				DisplayName: metric.Label,
				IsStacked:   metric.Stacked,
			}
			payload.Metrics = append(payload.Metrics, metricPayload)
		}

		payloads = append(payloads, payload)
	}

	return payloads
}

func (g *pluginGenerator) collectValues() (Values, error) {
	pluginMetaEnv := pluginConfigurationEnvName + "="
	stdout, stderr, _, err := g.Config.Command.RunWithEnv([]string{pluginMetaEnv})

	if stderr != "" {
		pluginLogger.Infof("command %s outputted to STDERR: %q", g.Config.Command.CommandString(), stderr)
	}
	if err != nil {
		pluginLogger.Errorf("Failed to execute command %s (skip these metrics):\n", g.Config.Command.CommandString())
		return nil, err
	}

	results := make(map[string]float64, 0)
	for _, line := range strings.Split(stdout, "\n") {
		// Key, value, timestamp
		// ex.) tcp.CLOSING 0 1397031808
		items := strings.Fields(line)
		if len(items) < 3 {
			continue
		}

		key := items[0]

		if g.Config.IncludePattern != nil && !g.Config.IncludePattern.MatchString(key) {
			continue
		}

		if g.Config.ExcludePattern != nil && g.Config.ExcludePattern.MatchString(key) {
			continue
		}

		value, err := strconv.ParseFloat(items[1], 64)
		if err != nil {
			pluginLogger.Warningf("Failed to parse values: %s", err)
			continue
		}

		results[pluginPrefix+key] = value
	}

	return results, nil
}
