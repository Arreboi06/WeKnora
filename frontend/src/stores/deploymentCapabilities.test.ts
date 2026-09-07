import assert from 'node:assert/strict'
import test from 'node:test'

import { isDeploymentCapabilitySupported, type DeploymentCapabilityMap } from '../config/deploymentCapabilities'

test('T2-M01A workbench store fallback data remains fail-closed after fetch errors', () => {
  const failedFetchCapabilities: DeploymentCapabilityMap = {}

  assert.equal(isDeploymentCapabilitySupported(failedFetchCapabilities, 'sandbox.workbench' as never), false)
})
