package codefacts

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiscoveryKeepsRoutesRequestsAndUnknownBoundariesSeparate(t *testing.T) {
	if _, err := ModulePath(); err != nil {
		t.Skip(err)
	}
	parsed, err := Analyze(context.Background(), "custom/entry.tsx", `
const routes = [{path: '/order', element: <Screen/>}];
createBrowserRouter(routes);
request('/orders', {method: 'POST', data: buildParams(state)});
axios.get('/orders', {params: {id}});
fetch(dynamicPath, options);
unknownTransport.send({payload});
export const Screen = () => <button {...runtimeButtons} disabled={loading} onClick={save}>Save</button>;
`)
	require.NoError(t, err)
	discovery := Discover(parsed.Nodes)
	counts := map[string]int{}
	for _, candidate := range discovery.Candidates {
		counts[candidate.Kind]++
		if candidate.Kind == "request" || candidate.Kind == "route_registration" || candidate.Kind == "route" {
			require.NotEmpty(t, candidate.Unknowns)
		}
	}
	require.Equal(t, 1, counts["route"])
	require.Equal(t, 1, counts["route_registration"])
	require.Equal(t, 3, counts["request"])
	require.Equal(t, 1, counts["interaction"])
	require.NotEmpty(t, discovery.Boundaries)
	// Unknown transports stay traceable generic call nodes, never fabricated requests.
	found := false
	for _, node := range parsed.Nodes {
		found = found || node.Kind == "call" && node.Callee == "unknownTransport.send"
	}
	require.True(t, found)
}
