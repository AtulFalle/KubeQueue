import Link from 'next/link';

export default function SessionExpiredPage() {
  return (
    <main className="page-shell narrow">
      <section className="panel">
        <h1>Your session expired</h1>
        <p>Sign in again to continue securely.</p>
        <Link className="button primary" href="/login">
          Sign in
        </Link>
      </section>
    </main>
  );
}
