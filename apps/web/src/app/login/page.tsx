import Link from 'next/link';

import { enabledLoginMethods } from '../../lib/identity-provider-data';
import { safeReturnTo } from '../../lib/server-session';

type LoginPageProps = {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
};

export default async function LoginPage({ searchParams }: LoginPageProps) {
  const query = await searchParams;
  const returnTo = safeReturnTo(typeof query.returnTo === 'string' ? query.returnTo : '/');
  const methods = await enabledLoginMethods().catch(() => []);
  const localEnabled = methods.some((method) => method.type === 'LOCAL');
  const oidcMethods = methods.filter((method) => method.type === 'OIDC');
  const invalidCredentials = query.error === 'invalid_credentials';
  return (
    <main className="page-shell narrow">
      <section className="panel">
        <p className="eyebrow">Secure sign in</p>
        <h1>Sign in to KubeQueue</h1>
        {methods.length === 0 ? <p role="alert">No login method is currently available.</p> : null}
        {localEnabled ? (
          <form className="setup-form" action="/auth/local" method="post">
            <input type="hidden" name="returnTo" value={returnTo} />
            <label>
              Username
              <input name="username" autoComplete="username" required maxLength={128} />
            </label>
            <label>
              Password
              <input
                name="password"
                type="password"
                autoComplete="current-password"
                required
                maxLength={128}
              />
            </label>
            {invalidCredentials ? (
              <p className="form-error" role="alert">
                Username or password is incorrect.
              </p>
            ) : null}
            <button className="button primary" type="submit">
              Sign in with local account
            </button>
          </form>
        ) : null}
        {oidcMethods.length > 0 ? (
          <div className="login-methods" aria-label="Single sign-on options">
            <h2>Single sign-on</h2>
            {oidcMethods.map((method) => (
              <Link
                className="button ghost"
                href={`/auth/login?provider=${encodeURIComponent(method.id)}&returnTo=${encodeURIComponent(returnTo)}`}
                key={method.id}
              >
                Continue with {method.label}
              </Link>
            ))}
          </div>
        ) : null}
      </section>
    </main>
  );
}
