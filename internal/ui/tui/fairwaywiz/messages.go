package fairwaywiz

import "github.com/shipyard-auto/shipyard/internal/fairwayctl"

type routesLoadedMsg struct {
	routes []fairwayctl.Route
	err    error
}

type statusLoadedMsg struct {
	status fairwayctl.StatusInfo
	routes []fairwayctl.Route
	err    error
}

type routeSubmitMsg struct {
	route fairwayctl.Route
	err   error
}

type routeDeleteMsg struct {
	path string
	err  error
}
