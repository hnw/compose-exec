package compose

import (
	"fmt"

	"github.com/compose-spec/compose-go/v2/types"
)

func findService(project *types.Project, name string) (types.ServiceConfig, error) {
	for _, s := range project.Services {
		if s.Name == name {
			return s, nil
		}
	}
	return types.ServiceConfig{}, fmt.Errorf("compose: service %q not found", name)
}
