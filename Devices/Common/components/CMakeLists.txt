idf_component_register(
    SRCS
        "wifi_manager/wifi_manager.c"
        "mqtt_manager/mqtt_manager.c"

    INCLUDE_DIRS
        "wifi_manager/include"
        "mqtt_manager/include"

    REQUIRES
        esp_wifi
        esp_event
        esp_netif
        wpa_supplicant
        mqtt
        freertos
)