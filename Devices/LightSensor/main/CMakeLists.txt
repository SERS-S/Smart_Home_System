idf_component_register(
    SRCS "main.c"
    INCLUDE_DIRS "."
    REQUIRES
        esp_adc
        mqtt
        esp_wifi
        wpa_supplicant
        esp_event
        esp_netif
        nvs_flash
        cjson
)