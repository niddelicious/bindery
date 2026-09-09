import { request } from './core'

export interface DownloadClient {
  id: number
  name: string
  type: string
  host: string
  port: number
  // apiKey and password are write-only (#2213). Every response blanks them and
  // reports whether one is stored through the two booleans below, so nothing in
  // the browser ever holds a download-client secret it did not just type.
  apiKey: string
  username: string
  password: string
  apiKeyConfigured: boolean
  passwordConfigured: boolean
  useSsl: boolean
  urlBase: string
  category: string
  // categoryAudiobook overrides category for audiobook grabs only.
  // Optional; when empty (the default for pre-#700 rows) audiobook grabs
  // fall back to `category`.
  categoryAudiobook?: string
  mediaType?: string
  pathRemap?: string
  enabled: boolean
  health?: DownloadClientHealth
}

// DownloadClientUpdate is the edit payload. Omitting apiKey or password (or
// sending an empty string) keeps whatever is stored; removing one takes the
// matching clear flag. Sending a non-empty value together with its own clear
// flag is rejected with a 400.
export type DownloadClientUpdate = Partial<DownloadClient> & {
  clearApiKey?: boolean
  clearPassword?: boolean
}

export interface DownloadClientHealth {
  // 'unknown' means the client type exposes no completed-downloads path
  // Bindery can introspect (SABnzbd, Transmission, Deluge), so imports are
  // unverified rather than known-good. Before #2029 those types stored a
  // fabricated 'ok'.
  status: 'ok' | 'checking' | 'error' | 'unknown'
  message: string
}

// PathVisibility is returned by the Test action (#1182) when the client's
// completed-downloads path is introspectable. status 'ok' means Bindery can
// read it; 'warning' means it connected but the resolved path isn't visible
// (the silent-import-failure case). Client types whose completed path can't be
// introspected omit the field entirely.
export interface PathVisibility {
  status: 'ok' | 'warning'
  message?: string
  path?: string
}

export const downloadClientsApi = {
  // Download clients
  listDownloadClients: () => request<DownloadClient[]>('/downloadclient'),
  addDownloadClient: (data: Partial<DownloadClient>) => request<DownloadClient>('/downloadclient', { method: 'POST', body: JSON.stringify(data) }),
  updateDownloadClient: (id: number, data: DownloadClientUpdate) => request<DownloadClient>(`/downloadclient/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
  deleteDownloadClient: (id: number) => request<void>(`/downloadclient/${id}`, { method: 'DELETE' }),
  testDownloadClient: (id: number) => request<{ message: string; health?: DownloadClientHealth; pathVisibility?: PathVisibility }>(`/downloadclient/${id}/test`, { method: 'POST' }),
  // Test an unsaved download-client config (Add/Edit form Test button). Does
  // not persist; mirrors testDownloadClient's response (minus async health).
  // Passing the saved client's id lets the backend fill in a credential the
  // caller left blank, but only while type, host, port, TLS and URL base still
  // match the saved row (#2213).
  testDownloadClientConfig: (data: DownloadClientUpdate) =>
    request<{ message: string; pathVisibility?: PathVisibility }>('/downloadclient/test', { method: 'POST', body: JSON.stringify(data) }),
}
