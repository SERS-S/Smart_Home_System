idf_component_register(
    SRCS
        "wifi_manager/wifi_manager.c"
        "mqtt_manager/mqtt_manager.c"
        "button/button.c"

    INCLUDE_DIRS
        "wifi_manager/include"
        "mqtt_manager/include"
        "button/include"

    REQUIRES
        esp_wifi
        esp_event
        esp_netif
        esp_driver_gpio
        wpa_supplicant
        mqtt
        freertos
)