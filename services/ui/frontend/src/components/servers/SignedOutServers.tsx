import { docsPath } from "../../api/config";
import { Button } from "../../ui/Button";
import { Icon } from "../../ui/Icon";

type SignedOutServersProps = {
  onSignIn: () => void;
};

const POINTS = [
  {
    icon: "server" as const,
    title: "Server discovery",
    body: "Every MCP server deployed into the namespaces your account can read, with its endpoint and rollout state.",
  },
  {
    icon: "shield" as const,
    title: "Tool governance",
    body: "Required trust, declared side effects, risk, and drift between what a server declares and what it serves.",
  },
  {
    icon: "activity" as const,
    title: "Runtime visibility",
    body: "Gateway decisions and usage for the servers and teams you belong to.",
  },
];

export function SignedOutServers({ onSignIn }: SignedOutServersProps) {
  return (
    <section className="landing" aria-labelledby="catalog-signed-out-title" data-testid="catalog-signed-out">
      <div className="landing-hero">
        <div className="landing-intro">
          <span className="landing-eyebrow">MCP Runtime platform</span>
          <h1 className="landing-title" id="catalog-signed-out-title">
            See what’s running. Govern every tool call.
          </h1>
          <p className="landing-lede">
            Discover deployed MCP servers, inspect their tools, and understand how the gateway applies policy.
            Sign in to view the namespaces your account can access.
          </p>
          <div className="landing-actions">
            <Button variant="primary" icon="login" onClick={onSignIn} data-testid="landing-signin-button">
              Sign in to your workspace
            </Button>
            <a className="landing-doc-link" href={docsPath()} target="_blank" rel="noreferrer">
              Read the platform docs <span aria-hidden="true">↗</span>
            </a>
          </div>
        </div>
        <ol className="landing-journey" aria-label="MCP Runtime workflow">
          {["Deploy", "Discover", "Govern", "Observe"].map((step, index) => (
            <li key={step}>
              <span>{String(index + 1).padStart(2, "0")}</span>
              <strong>{step}</strong>
            </li>
          ))}
        </ol>
      </div>

      <div className="landing-points">
        {POINTS.map((point) => (
          <div className="landing-point" key={point.title}>
            <h2>
              <Icon name={point.icon} size={15} />
              {point.title}
            </h2>
            <p>{point.body}</p>
          </div>
        ))}
      </div>

    </section>
  );
}
