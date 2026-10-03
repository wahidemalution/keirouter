import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Outlet } from "react-router-dom";
import { fetchPortalStatus } from "../../lib/api";
import { useBranding } from "../../contexts/BrandingContext";
import { Card, Button, Spinner } from "../../components/ui";
import { TurnstileWidget, useTurnstileSiteKey } from "../../components/TurnstileWidget";

function SignInCard() {
  const { branding, logoSrc } = useBranding();
  const siteKey = useTurnstileSiteKey();
  const [token, setToken] = useState("");
  const failed = new URLSearchParams(window.location.search).get("turnstile") === "failed";
  const ready = !siteKey || !!token;

  const start = () => {
    if (!ready) return;
    window.location.href = siteKey
      ? `/portal/auth/google/start?token=${encodeURIComponent(token)}`
      : "/portal/auth/google/start";
  };

  return (
    <div className="flex min-h-screen items-center justify-center bg-[var(--bg)] p-4 md:p-8">
      <Card className="w-full max-w-md p-8 md:p-10 text-center">
        <div className="mx-auto mb-6 flex h-16 w-16 items-center justify-center rounded-2xl bg-[var(--bg-subtle)] ring-1 ring-inset ring-[var(--border)]">
          <img src={logoSrc} alt={branding.name || "KeiRouter"} className="h-8 object-contain" />
        </div>
        <h1 className="mb-2 text-2xl font-display tracking-tight text-[var(--text)]">Portal Access</h1>
        <p className="mb-8 text-sm text-[var(--text-muted)]">
          {branding.tagline || "Sign in with Google to manage your API key and monitor usage."}
        </p>
        <TurnstileWidget siteKey={siteKey} onToken={setToken} className="mb-4 flex justify-center" />
        {failed && (
          <p className="mb-3 text-xs text-[color:var(--color-danger)]">
            Verification failed. Please complete the challenge and try again.
          </p>
        )}
        <Button className="w-full h-11 text-base font-medium" onClick={start} disabled={!ready}>
          Sign in with Google
        </Button>
      </Card>
    </div>
  );
}

export function PortalRootPage() {
  const { data: status, isLoading } = useQuery({
    queryKey: ["portal-status"],
    queryFn: fetchPortalStatus,
    retry: false,
  });
  if (isLoading) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-[var(--bg)]">
        <Spinner />
      </div>
    );
  }
  if (!status?.authenticated) return <SignInCard />;
  return <Outlet />;
}
