import type { GlobalThemeOverrides } from 'naive-ui'

export const themeOverrides: GlobalThemeOverrides = {
  common: {
    primaryColor: '#d95f8d',
    primaryColorHover: '#e579a1',
    primaryColorPressed: '#bd4e79',
    primaryColorSuppl: '#d95f8d',
    borderRadius: '10px',
  },
  Button: {
    borderRadiusMedium: '10px',
  },
  Input: {
    borderRadius: '12px',
  },
  Select: {
    peers: {
      InternalSelection: {
        borderRadius: '10px',
      },
    },
  },
}
