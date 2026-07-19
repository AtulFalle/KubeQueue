import { redirect } from 'next/navigation';

import { currentBrowserSession } from '../../lib/server-session';

export const dynamic = 'force-dynamic';

export default async function LogoutPage() {
  const session = await currentBrowserSession();
  if (!session) redirect('/login');
  return (
    <main className="page-shell narrow">
      <section className="panel">
        <h1>Sign out?</h1>
        <p>This revokes your KubeQueue browser session on every replica.</p>
        <form action="/auth/logout" method="post">
          <input type="hidden" name="csrfToken" value={session.csrfToken} />
          <button className="button primary" type="submit">
            Sign out
          </button>
        </form>
      </section>
    </main>
  );
}
