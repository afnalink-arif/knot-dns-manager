import { render } from "solid-js/web";
import { Router, Route, Navigate } from "@solidjs/router";
import { Component, JSX } from "solid-js";
import { isLoggedIn } from "./lib/auth";
import { initTheme } from "./lib/theme";
import "./app.css";

// Stamp data-theme before the first paint so the app never flashes the wrong theme.
initTheme();

import LoginPage from "./routes/login";
import Overview from "./routes/index";
import QueryMetrics from "./routes/queries";
import CachePage from "./routes/cache";
import DNSSECPage from "./routes/dnssec";
import SystemPage from "./routes/system";
import UpstreamsPage from "./routes/upstreams";
import QueryLogs from "./routes/logs";
import AlertsPage from "./routes/alerts";
import SettingsPage from "./routes/settings";
import ClusterPage from "./routes/cluster";
import FilteringPage from "./routes/filtering";
import DNSLookupPage from "./routes/dns-lookup";

// Auth guard wrapper
function Protected(props: { component: Component }): JSX.Element {
  if (!isLoggedIn()) {
    return <Navigate href="/login" />;
  }
  return <props.component />;
}

render(
  () => (
    <Router>
      <Route path="/login" component={LoginPage} />
      <Route path="/" component={() => <Protected component={Overview} />} />
      <Route path="/queries" component={() => <Protected component={QueryMetrics} />} />
      <Route path="/cache" component={() => <Protected component={CachePage} />} />
      <Route path="/dnssec" component={() => <Protected component={DNSSECPage} />} />
      <Route path="/system" component={() => <Protected component={SystemPage} />} />
      <Route path="/upstreams" component={() => <Protected component={UpstreamsPage} />} />
      <Route path="/logs" component={() => <Protected component={QueryLogs} />} />
      <Route path="/alerts" component={() => <Protected component={AlertsPage} />} />
      <Route path="/settings" component={() => <Protected component={SettingsPage} />} />
      <Route path="/cluster" component={() => <Protected component={ClusterPage} />} />
      <Route path="/filtering" component={() => <Protected component={FilteringPage} />} />
      <Route path="/dns-lookup" component={() => <Protected component={DNSLookupPage} />} />
    </Router>
  ),
  document.getElementById("app")!
);
