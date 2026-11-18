package harness

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// RelayKindAWS marks an aws service's read relay: the edge admits only S3
// and DynamoDB reads, rebuilds and re-signs them (edge.KindAWS).
const RelayKindAWS = "aws"

// awsInitLimits bound the one-shot container that hands the emulator's data
// volume to the run's user.
var awsInitLimits = ResolvedLimits{Memory: 64 << 20, CPUs: 0.5, PIDs: 16}

// awsHealthScript passes once the emulator reports every service named in
// its arguments available.
const awsHealthScript = `import json,sys,urllib.request as r
s=json.load(r.urlopen("http://127.0.0.1:4566/_ministack/health",timeout=4)).get("services",{})
sys.exit(0 if all(s.get(n)=="available" for n in sys.argv[1:]) else 1)`

// addAWSService adds an aws service's emulator, the container that readies
// its data volume, and the volume.
func addAWSService(doc *composeFile, f *File, spec RunSpec, name string) {
	svc := f.Services[name]
	a := svc.AWS
	image := AWSImage(a.Kind)
	if id, ok := spec.ServiceImages[name]; ok {
		image = id
	}
	vol := dataVolume(name) + ":" + awsDataDir
	env := map[string]string{}
	maps.Copy(env, svc.Env)
	auth := "false"
	if a.VerifiesSignatures() {
		auth = "true"
	}
	maps.Copy(env, map[string]string{
		"SERVICES":         strings.Join(a.EmulatorServices(), ","),
		"AUTH":             auth,
		"MINISTACK_REGION": a.RegionOrDefault(),
		"GATEWAY_PORT":     strconv.Itoa(AWSPort),
		"S3_PERSIST":       "1",
		"S3_DATA_DIR":      awsDataDir + "/s3",
		"LOG_LEVEL":        "WARNING",
	})
	// The emulator runs as the run's user under every hardening the app
	// has, on the inner network alone; it gets no proxy, so it has no way
	// out at all.
	emu := composeService{
		Image:       image,
		PullPolicy:  pullNever,
		User:        fmt.Sprintf("%d:%d", spec.UID, spec.GID),
		ReadOnly:    true,
		CapDrop:     []string{capDropAll},
		SecurityOpt: []string{noNewPrivileges},
		Tmpfs:       []string{"/tmp"},
		Environment: env,
		Volumes:     []string{vol},
		Networks:    []string{NetworkInner},
		Sysctls:     disableIPv6,
		DependsOn:   map[string]composeCondition{awsInitService(name): {Condition: "service_completed_successfully"}},
	}
	if svc.Ready != "" {
		emu.Healthcheck = &composeHealthcheck{Test: []string{"CMD-SHELL", svc.Ready}, Interval: "1s", Timeout: "5s", Retries: 60}
	} else {
		emu.Healthcheck = &composeHealthcheck{
			Test:     append([]string{"CMD", "python", "-c", awsHealthScript}, a.EmulatorServices()...),
			Interval: "1s", Timeout: "5s", Retries: 60,
		}
	}
	limited(&emu, f.ServiceLimits(name, spec.ServiceMemory))
	doc.Services[AWSEmuService(name)] = emu

	// A fresh named volume belongs to root; the emulator does not, so a
	// one-shot container with CHOWN alone hands it over first.
	init := composeService{
		Image:       image,
		PullPolicy:  pullNever,
		Entrypoint:  []string{"chown"},
		User:        "0:0",
		ReadOnly:    true,
		CapDrop:     []string{capDropAll},
		CapAdd:      []string{"CHOWN"},
		SecurityOpt: []string{noNewPrivileges},
		NetworkMode: "none",
		Volumes:     []string{vol},
		Command:     []string{fmt.Sprintf("%d:%d", spec.UID, spec.GID), awsDataDir},
	}
	limited(&init, awsInitLimits)
	doc.Services[awsInitService(name)] = init
	doc.Volumes[dataVolume(name)] = map[string]any{}
}

// AWSEnv is what every container of a run with the aws service name reads:
// the endpoints of the edge tap, the region, the fixed test key, the config
// and credentials files under AWSMount, and IMDS off.
func AWSEnv(name string, a *AWS) map[string]string {
	endpoint := awsEndpoint(name)
	region := a.RegionOrDefault()
	return map[string]string{
		"AWS_ENDPOINT_URL":                  endpoint,
		"AWS_ENDPOINT_URL_S3":               endpoint,
		"AWS_ENDPOINT_URL_DYNAMODB":         endpoint,
		"AWS_ENDPOINT_URL_DYNAMODB_STREAMS": endpoint,
		"AWS_ENDPOINT_URL_SQS":              endpoint,
		"AWS_ENDPOINT_URL_SNS":              endpoint,
		"AWS_REGION":                        region,
		"AWS_DEFAULT_REGION":                region,
		"AWS_ACCESS_KEY_ID":                 AWSAccessKeyID,
		"AWS_SECRET_ACCESS_KEY":             AWSSecretAccessKey,
		"AWS_CONFIG_FILE":                   AWSMount + "/config",
		"AWS_SHARED_CREDENTIALS_FILE":       AWSMount + "/credentials",
		"AWS_EC2_METADATA_DISABLED":         "true",
	}
}

func awsEndpoint(name string) string { return "http://" + name + ":" + strconv.Itoa(AWSPort) }

// awsFiles are the shared config (region, endpoint, path-style S3) and
// credentials files: the test key and nothing else.
func awsFiles(name string, a *AWS) map[string][]byte {
	return map[string][]byte{
		AWSConfigFile: []byte("[default]\nregion = " + a.RegionOrDefault() + "\nendpoint_url = " + awsEndpoint(name) +
			"\ns3 =\n    addressing_style = path\n"),
		AWSCredentialsFile: []byte("[default]\naws_access_key_id = " + AWSAccessKeyID + "\naws_secret_access_key = " + AWSSecretAccessKey + "\n"),
	}
}

// withAWS wires the scenario's aws service, if any, into the rest of the
// project: the edge takes the service's name on the inner network and taps
// it through to the emulator, and the app, the checks and the other
// services get AWSEnv (aiman's values win over a service's own) and the
// read-only AWSMount.
func withAWS(doc *composeFile, files map[string][]byte, f *File, sc *Scenario) {
	names := f.AWSServices(sc)
	if len(names) == 0 {
		return
	}
	name := names[0] // validation allows one per scenario
	a := f.Services[name].AWS
	maps.Copy(files, awsFiles(name, a))
	env := AWSEnv(name, a)
	mount := "./" + awsFilesDir + ":" + AWSMount + ":ro"
	apply := func(svc string) {
		s := doc.Services[svc]
		if s.Environment == nil {
			s.Environment = map[string]string{}
		}
		maps.Copy(s.Environment, env)
		s.Volumes = append(s.Volumes, mount)
		doc.Services[svc] = s
	}
	apply(ServiceApp)
	for svc, s := range doc.Services {
		if svc != ServiceCheckAdapter && slices.ContainsFunc(s.Profiles, func(p string) bool {
			return p == checksProfile || p == profileCheck || p == profileDebugCheck
		}) {
			apply(svc)
		}
	}
	for _, svc := range sc.Services {
		if f.Services[svc].AWS == nil {
			apply(svc)
		}
	}
	e := doc.Services[ServiceEdge]
	e.Command = append(e.Command, "--aws-tap", fmt.Sprintf("%s=%s:%d", name, AWSEmuService(name), AWSPort))
	e.Networks = withAlias(e.Networks, NetworkInner, name)
	doc.Services[ServiceEdge] = e
}

// withAlias adds alias to a service's attachment to network, turning the
// list form into the map form when needed.
func withAlias(networks any, network, alias string) map[string]composeAttach {
	out := map[string]composeAttach{}
	switch n := networks.(type) {
	case []string:
		for _, name := range n {
			out[name] = composeAttach{}
		}
	case map[string]composeAttach:
		maps.Copy(out, n)
	}
	at := out[network]
	at.Aliases = append(at.Aliases, alias)
	out[network] = at
	return out
}
