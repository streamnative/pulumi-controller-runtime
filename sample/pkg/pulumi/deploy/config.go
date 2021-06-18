package deploy


import (
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/config"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
)

func NewHostConfig(cfg config.Map, d config.Decrypter) HostConfig {
	return HostConfig{
		Config:    cfg,
		Decrypter: d,
	}
}

// HostConfig contains configuration data for hosted providers.
// Unlike deploy.Target, is not stack-specific to maximize host lifespan.
type HostConfig struct {
	Config    config.Map       // optional configuration key/value pairs.
	Decrypter config.Decrypter // decrypter for secret configuration values.
}

// GetPackageConfig returns the set of configuration parameters for the indicated package, if any.
func (t *HostConfig) GetPackageConfig(pkg tokens.Package) (resource.PropertyMap, error) {
	result := resource.PropertyMap{}
	if t == nil {
		return result, nil
	}

	for k, c := range t.Config {
		if tokens.Package(k.Namespace()) != pkg {
			continue
		}

		v, err := c.Value(t.Decrypter)
		if err != nil {
			return nil, err
		}

		propertyValue := resource.NewStringProperty(v)
		if c.Secure() {
			propertyValue = resource.MakeSecret(propertyValue)
		}
		result[resource.PropertyKey(k.Name())] = propertyValue
	}
	return result, nil
}
