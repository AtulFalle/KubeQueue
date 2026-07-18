import '@testing-library/jest-dom/vitest';

import { cleanup } from '@testing-library/react';
import { toHaveNoViolations } from 'jest-axe';
import { afterEach, expect, vi } from 'vitest';

expect.extend(toHaveNoViolations);

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

class EventSourceStub {
  addEventListener() {
    return undefined;
  }
  close() {
    return undefined;
  }
}

vi.stubGlobal('EventSource', EventSourceStub);
