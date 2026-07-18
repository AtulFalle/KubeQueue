import type { NextConfig } from 'next';

const nextConfig: NextConfig = {
  output: 'standalone',
  poweredByHeader: false,
  reactStrictMode: true,
  transpilePackages: ['@kubequeue/api-client'],
  async rewrites() {
    const apiOrigin = process.env.KUBEQUEUE_API_INTERNAL_URL ?? 'http://localhost:8080';

    return [
      {
        source: '/api/:path*',
        destination: `${apiOrigin}/api/:path*`,
      },
    ];
  },
};

export default nextConfig;
