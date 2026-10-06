import { useCallback, useState, type FormEvent } from "react";

import { GoogleSignInButton } from "./GoogleSignInButton";
import { Button } from "../ui/Button";
import { TextField } from "../ui/Field";
import { Icon } from "../ui/Icon";
import { PageHeader } from "../ui/PageHeader";
import { readRuntimeConfig } from "../api/config";
import type { LoginInput } from "../api/auth";

type SignInPanelProps = {
  onSubmit: (input: LoginInput) => Promise<void>;
  onCancel?: () => void;
  error: string;
  busy: boolean;
};

type Mode = "account" | "api-key";

export function SignInPanel({ onSubmit, onCancel, error, busy }: SignInPanelProps) {
  const [mode, setMode] = useState<Mode>("account");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [googleError, setGoogleError] = useState("");
  const [validation, setValidation] = useState<{ email?: string; password?: string; apiKey?: string }>({});
  const googleClientId = readRuntimeConfig().googleClientId;

  // GoogleSignInButton re-initialises when this identity changes, so it has to
  // be stable across renders or the GSI button is rebuilt on every keystroke.
  const handleGoogleCredential = useCallback(
    (idToken: string) => {
      setGoogleError("");
      void onSubmit({ idToken });
    },
    [onSubmit]
  );

  function switchMode(next: Mode) {
    setMode(next);
    setValidation({});
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    // Guard against a second submit while the first is still in flight.
    if (busy) {
      return;
    }

    if (mode === "api-key") {
      const key = apiKey.trim();
      if (!key) {
        setValidation({ apiKey: "Enter an API key." });
        return;
      }
      setValidation({});
      await onSubmit({ apiKey: key });
      return;
    }

    const next: { email?: string; password?: string } = {};
    if (!email.trim()) {
      next.email = "Enter the email address for your platform account.";
    }
    if (!password) {
      next.password = "Enter your password.";
    }
    setValidation(next);
    if (next.email || next.password) {
      return;
    }
    setGoogleError("");
    await onSubmit({ email: email.trim(), password });
  }

  return (
    <div className="signin-layout">
      <div>
        <PageHeader
          title="Sign in to MCP Runtime"
          description="The server catalog, tool governance data, and platform administration are scoped to your account."
        />

        <div className="signin-card">
          <fieldset className="segmented" style={{ marginBottom: "var(--space-4)" }}>
            <legend className="visually-hidden">Sign-in method</legend>
            <button
              type="button"
              className="segment"
              aria-pressed={mode === "account"}
              data-testid="signin-mode-account"
              onClick={() => switchMode("account")}
            >
              Account
            </button>
            <button
              type="button"
              className="segment"
              aria-pressed={mode === "api-key"}
              data-testid="signin-mode-api-key"
              onClick={() => switchMode("api-key")}
            >
              API key
            </button>
          </fieldset>

          <form className="signin-form" onSubmit={handleSubmit} data-testid="login-form" noValidate>
            {mode === "account" ? (
              <>
                <TextField
                  label="Email"
                  type="email"
                  autoComplete="username"
                  value={email}
                  error={validation.email}
                  data-testid="login-email"
                  onChange={(event) => {
                    setEmail(event.target.value);
                    if (validation.email) setValidation((v) => ({ ...v, email: undefined }));
                  }}
                />
                <TextField
                  label="Password"
                  type="password"
                  autoComplete="current-password"
                  value={password}
                  error={validation.password}
                  data-testid="login-password"
                  onChange={(event) => {
                    setPassword(event.target.value);
                    if (validation.password) setValidation((v) => ({ ...v, password: undefined }));
                  }}
                />
              </>
            ) : (
              <TextField
                label="API key"
                type="password"
                autoComplete="off"
                spellCheck={false}
                value={apiKey}
                error={validation.apiKey}
                hint="An MCP Runtime dashboard API key. It is exchanged for a session cookie and never stored in the browser."
                data-testid="login-api-key"
                onChange={(event) => {
                  setApiKey(event.target.value);
                  if (validation.apiKey) setValidation((v) => ({ ...v, apiKey: undefined }));
                }}
              />
            )}

            {error || googleError ? (
              <p className="field-error" role="alert" data-testid="login-error">
                <Icon name="alert" size={13} />
                {error || googleError}
              </p>
            ) : null}

            <div className="form-footer">
              {onCancel ? (
                <Button variant="ghost" onClick={onCancel} disabled={busy}>
                  Cancel
                </Button>
              ) : null}
              <Button type="submit" variant="primary" busy={busy} data-testid="login-submit">
                {busy ? "Signing in…" : "Continue"}
              </Button>
            </div>
          </form>

          {googleClientId ? (
            <>
              <div className="signin-divider">or</div>
              <GoogleSignInButton onCredential={handleGoogleCredential} onError={setGoogleError} />
            </>
          ) : null}
        </div>
      </div>
    </div>
  );
}
