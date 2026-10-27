import { describe, expect, it } from 'vitest'
import { ApiError, isNoDownloadClientError } from './core'

describe('isNoDownloadClientError', () => {
  it('matches the 400 the backend sends for a missing download client, whatever the wording', () => {
    for (const error of [
      'no enabled download client configured — add a download client',
      'no enabled usenet download client configured',
      'something the backend words differently next release',
    ]) {
      expect(isNoDownloadClientError(new ApiError(400, { error, code: 'no_download_client' }, 'Bad Request'))).toBe(true)
    }
  })

  it('does not match on the message alone', () => {
    const lookalike = new ApiError(400, { error: 'no enabled usenet download client configured' }, 'Bad Request')
    expect(isNoDownloadClientError(lookalike)).toBe(false)
  })

  it('does not match other statuses or other codes', () => {
    expect(isNoDownloadClientError(new ApiError(502, { error: 'failed to send the release to the download client' }, 'Bad Gateway'))).toBe(false)
    expect(isNoDownloadClientError(new ApiError(502, { code: 'no_download_client' }, 'Bad Gateway'))).toBe(false)
    expect(isNoDownloadClientError(new ApiError(400, { error: 'guid and nzbUrl required', code: 'other' }, 'Bad Request'))).toBe(false)
  })

  it('does not match errors that are not ApiErrors', () => {
    expect(isNoDownloadClientError(new Error('no enabled download client configured'))).toBe(false)
    expect(isNoDownloadClientError(undefined)).toBe(false)
    expect(isNoDownloadClientError('no enabled download client configured')).toBe(false)
  })
})
