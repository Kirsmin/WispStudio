import { type GlobalThemeOverrides } from 'naive-ui'

export const themeOverrides: GlobalThemeOverrides = {
  common: {
    primaryColor: '#8a4fc8',
    primaryColorHover: '#7a3fb8',
    primaryColorPressed: '#6a2fa8',
    borderRadius: '8px',
    borderRadiusSmall: '4px',
  },
  Button: {
    borderRadiusTiny: '8px',
    borderRadiusSmall: '8px',
    borderRadiusMedium: '8px',
    borderRadiusLarge: '8px',
  },
  Input: {
    borderRadius: '8px',
  },
  Select: {
    peers: {
      InternalSelection: {
        borderRadius: '8px',
      }
    }
  },
}
