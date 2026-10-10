import { QueryClient } from '@tanstack/vue-query'

export function createModernQueryClient() {
  return new QueryClient({
    defaultOptions: {
      // 使用查询库默认的 visibilitychange 监听与页面挂载刷新，不监听窗口 focus。
      // staleTime 只决定事件触发时是否更新缓存，不会产生定时请求。
      queries: {
        retry: false,
        staleTime: 0,
        // 限额用量会随请求变化，这几类查询每 5 秒刷新一次；页面不可见时不刷新。
        refetchInterval: (query): number | false => {
          if (query.queryKey[0] !== 'modern') return false
          const section = query.queryKey[1]
          return section === 'group-credentials' ||
            section === 'access-keys' ||
            section === 'access-key-detail' ||
            section === 'credential-detail'
            ? 5000
            : false
        },
        refetchIntervalInBackground: false,
        refetchOnWindowFocus: true,
        refetchOnReconnect: false,
        refetchOnMount: true,
      },
      mutations: { retry: false },
    },
  })
}
