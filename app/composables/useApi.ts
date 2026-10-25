import createClient from 'openapi-fetch'
import type { paths } from '~/types/api'
export function useApi() {
  const token = useState('adminToken', () => '')
  const client = createClient<paths>({
    baseUrl: '',
    fetch: async (input) => {
      const request = new Request(input)
      request.headers.set('Authorization', `Bearer ${token.value}`)
      const response = await fetch(request)
      if (response.status === 401) token.value = ''
      return response
    },
  })
  return { client, token }
}
