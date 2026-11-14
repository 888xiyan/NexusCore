// NexusCore HTTP Client Utility

export interface ApiResponse<T = any> {
  data: T
  status: number
}

export class ApiError extends Error {
  status: number
  details?: any

  constructor(message: string, status: number, details?: any) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.details = details
  }
}

async function request<T = any>(url: string, options: RequestInit = {}): Promise<T> {
  const defaultHeaders: Record<string, string> = {
    'Accept': 'application/json',
  }

  if (options.body && typeof options.body === 'string') {
    defaultHeaders['Content-Type'] = 'application/json'
  }

  const response = await fetch(url, {
    ...options,
    headers: {
      ...defaultHeaders,
      ...options.headers,
    },
  })

  // Handle No Content (204)
  if (response.status === 204) {
    return {} as T
  }

  const contentType = response.headers.get('content-type') || ''
  let responseData: any

  if (contentType.includes('application/json')) {
    try {
      responseData = await response.json()
    } catch {
      responseData = null
    }
  } else {
    responseData = await response.text()
  }

  if (!response.ok) {
    const errorMsg =
      (responseData && typeof responseData === 'object' && responseData.error) ||
      (typeof responseData === 'string' && responseData) ||
      `HTTP error ${response.status}: ${response.statusText}`
    throw new ApiError(errorMsg, response.status, responseData)
  }

  return responseData as T
}

export const apiClient = {
  get: <T = any>(url: string, headers?: Record<string, string>) =>
    request<T>(url, { method: 'GET', headers }),

  post: <T = any>(url: string, data?: any, headers?: Record<string, string>) =>
    request<T>(url, {
      method: 'POST',
      body: data !== undefined ? JSON.stringify(data) : undefined,
      headers,
    }),

  put: <T = any>(url: string, data?: any, headers?: Record<string, string>) =>
    request<T>(url, {
      method: 'PUT',
      body: data !== undefined ? JSON.stringify(data) : undefined,
      headers,
    }),

  del: <T = any>(url: string, headers?: Record<string, string>) =>
    request<T>(url, { method: 'DELETE', headers }),
}
